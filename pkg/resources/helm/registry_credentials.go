// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	registryauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Read-only selection follows pinned ORAS: host helper, configured store,
// detected native store only when no auth is configured, then file credentials.
// FileStore preserves its auth/identitytoken/registrytoken and Docker Hub rules.
// Native execution is local because ORAS Output has no bounded pipe wait.
type registryCredentialStore struct {
	file    *credentials.FileStore
	helpers map[string]string
	helper  string
}

func newRegistryCredentialStore(path string) (*registryCredentialStore, error) {
	file, err := credentials.NewFileStore(path)
	if err != nil {
		return nil, auth.RedactError(err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, auth.RedactError(err)
	}
	var settings struct {
		Helpers map[string]string          `json:"credHelpers"`
		Helper  string                     `json:"credsStore"`
		Auths   map[string]json.RawMessage `json:"auths"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&settings); err != nil && !errors.Is(err, io.EOF) {
		return nil, auth.RedactError(err)
	}
	helper := settings.Helper
	if helper == "" && len(settings.Helpers) == 0 && len(settings.Auths) == 0 {
		switch runtime.GOOS {
		case "linux":
			helper = "secretservice"
			if _, err := exec.LookPath("pass"); err == nil {
				helper = "pass"
			}
		case "darwin":
			helper = "osxkeychain"
		case "windows":
			helper = "wincred"
		}
		if helper != "" {
			if _, err := exec.LookPath("docker-credential-" + helper); err != nil {
				helper = ""
			}
		}
	}
	return &registryCredentialStore{file: file, helpers: settings.Helpers, helper: helper}, nil
}
func (s *registryCredentialStore) get(ctx context.Context, host string) (registryauth.Credential, error) {
	if err := ctx.Err(); err != nil {
		return registryauth.EmptyCredential, err
	}
	helper := s.helpers[host]
	if helper == "" {
		helper = s.helper
	}
	if helper != "" {
		return runRegistryHelper(ctx, helper, host)
	}
	cred, err := s.file.Get(ctx, host)
	return cred, auth.RedactError(err)
}
func registryCredentialLookup(helmPath string) (registryauth.CredentialFunc, error) {
	primary, err := newRegistryCredentialStore(helmPath)
	if err != nil {
		return nil, err
	}
	stores := []*registryCredentialStore{primary}
	dockerDir := os.Getenv("DOCKER_CONFIG")
	if dockerDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dockerDir = filepath.Join(home, ".docker")
		}
	}
	if dockerDir != "" {
		if fallback, err := newRegistryCredentialStore(filepath.Join(dockerDir, "config.json")); err == nil {
			stores = append(stores, fallback)
		}
	}
	return func(ctx context.Context, host string) (registryauth.Credential, error) {
		host = credentials.ServerAddressFromHostname(host)
		if host == "" {
			return registryauth.EmptyCredential, nil
		}
		for _, store := range stores {
			cred, err := store.get(ctx, host)
			if err != nil || cred != registryauth.EmptyCredential {
				return cred, err
			}
		}
		return registryauth.EmptyCredential, nil
	}, nil
}
func runRegistryHelper(ctx context.Context, helper, host string) (registryauth.Credential, error) {
	cmd := exec.CommandContext(ctx, "docker-credential-"+helper, "get")
	cmd.Stdin = strings.NewReader(host)
	// Helpers may emit credentials in failures; never pass their streams through.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	configureRegistryHelper(cmd)
	output, err := cmd.Output()
	if err != nil {
		stopRegistryHelper(cmd)
	}
	if ctx.Err() != nil {
		return registryauth.EmptyCredential, ctx.Err()
	}
	if err != nil {
		if strings.TrimSpace(string(output)) == "credentials not found in native keychain" {
			return registryauth.EmptyCredential, nil
		}
		return registryauth.EmptyCredential, auth.RedactError(err)
	}
	var value struct {
		Username string
		Secret   string
	}
	if err := json.Unmarshal(output, &value); err != nil {
		return registryauth.EmptyCredential, auth.RedactError(err)
	}
	if value.Username == "<token>" {
		return registryauth.Credential{RefreshToken: value.Secret}, nil
	}
	return registryauth.Credential{Username: value.Username, Password: value.Secret}, nil
}
