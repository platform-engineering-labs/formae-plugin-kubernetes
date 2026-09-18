//go:build unit

package helm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	registryauth "oras.land/oras-go/v2/registry/remote/auth"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
)

func TestOCIConfiguredCredentialHelperHonorsCallbackDeadline(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process group cleanup platform")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	survivor := filepath.Join(dir, "survived")
	script := fmt.Sprintf("#!/bin/sh\necho started > '%s'\n(sleep 0.6; echo survived > '%s') &\nwait\n", marker, survivor)
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-budget"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", dir)
	registryFile := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(registryFile, []byte(`{"credsStore":"budget"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HELM_REGISTRY_CONFIG", registryFile)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	authorizer, err := newRegistryAuthorizer(ctx, cli.New())
	if err != nil {
		t.Fatal(err)
	}
	client, err := registry.NewClient(registry.ClientOptAuthorizer(authorizer), registry.ClientOptPlainHTTP(), registry.ClientOptCredentialsFile(registryFile))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client.Tags(server.Listener.Addr().String() + "/chart")
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("fixture credential helper was never invoked: %v (Tags: %v)", statErr, err)
	}
	if err == nil {
		t.Fatal("hanging helper unexpectedly succeeded")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("credential helper escaped callback budget: %s", time.Since(start))
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(survivor); !os.IsNotExist(err) {
		t.Fatal("helper descendant survived callback cancellation")
	}
}

func TestOCIConfiguredCredentialsStillAuthenticate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	registryFile := filepath.Join(dir, "registry.json")
	t.Setenv("HELM_REGISTRY_CONFIG", registryFile)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "user" || password != "pass" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"chart","tags":["1.0.0"]}`)
	}))
	defer server.Close()
	raw := fmt.Sprintf(`{"auths":{%q:{"auth":"dXNlcjpwYXNz"}}}`, server.Listener.Addr().String())
	if err := os.WriteFile(registryFile, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	authorizer, err := newRegistryAuthorizer(ctx, cli.New())
	if err != nil {
		t.Fatal(err)
	}
	client, err := registry.NewClient(registry.ClientOptAuthorizer(authorizer), registry.ClientOptPlainHTTP(), registry.ClientOptCredentialsFile(registryFile))
	if err != nil {
		t.Fatal(err)
	}
	tags, err := client.Tags(server.Listener.Addr().String() + "/chart")
	if err != nil || len(tags) != 1 || tags[0] != "1.0.0" {
		t.Fatalf("configured auth lost: %v %v", tags, err)
	}
}

// Exercise selection at the authorizer's actual credential boundary, including
// identity tokens which ORAS consumes during the later token exchange.
func TestOCIReadOnlyCredentialCompatibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture")
	}
	for _, tc := range []struct {
		name, primary, fallback string
		want                    registryauth.Credential
		fail                    bool
	}{
		{name: "host helper overrides store and file", primary: `{"credHelpers":{"registry.test":"host"},"credsStore":"store","auths":{"registry.test":{"auth":"ZmlsZTpwYXNz"}}}`, want: registryauth.Credential{Username: "host", Password: "secret"}},
		{name: "configured store overrides file", primary: `{"credsStore":"store","auths":{"registry.test":{"auth":"ZmlsZTpwYXNz"}}}`, want: registryauth.Credential{Username: "store", Password: "secret"}},
		{name: "Helm file precedes Docker", primary: `{"auths":{"registry.test":{"auth":"aGVsbTpwYXNz"}}}`, fallback: `{"credsStore":"store"}`, want: registryauth.Credential{Username: "helm", Password: "pass"}},
		{name: "Docker fallback after missing host", primary: `{"auths":{"other.test":{}}}`, fallback: `{"auths":{"registry.test":{"auth":"ZG9ja2VyOnBhc3M="}}}`, want: registryauth.Credential{Username: "docker", Password: "pass"}},
		{name: "Docker fallback after helper not found", primary: `{"credsStore":"missing"}`, fallback: `{"auths":{"registry.test":{"identitytoken":"refresh","registrytoken":"access"}}}`, want: registryauth.Credential{RefreshToken: "refresh", AccessToken: "access"}},
		{name: "static identity tokens", primary: `{"auths":{"registry.test":{"identitytoken":"refresh","registrytoken":"access"}}}`, want: registryauth.Credential{RefreshToken: "refresh", AccessToken: "access"}},
		{name: "native identity token", primary: `{"credsStore":"token"}`, want: registryauth.Credential{RefreshToken: "native-refresh"}},
		{name: "helper error stops fallback and is redacted", primary: `{"credsStore":"error"}`, fallback: `{"credsStore":"store"}`, fail: true},
		{name: "invalid helper output is redacted", primary: `{"credsStore":"invalid"}`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("DOCKER_CONFIG", dir)
			for name, body := range map[string]string{
				"host":    `printf '%s' '{"Username":"host","Secret":"secret"}'`,
				"store":   `printf '%s' '{"Username":"store","Secret":"secret"}'`,
				"token":   `printf '%s' '{"Username":"<token>","Secret":"native-refresh"}'`,
				"missing": `printf '%s' 'credentials not found in native keychain'; exit 1`,
				"error":   `printf '%s' 'sensitive-helper-output'; printf '%s' 'sensitive-helper-stderr' >&2; exit 1`,
				"invalid": `printf '%s' 'sensitive-helper-output'`,
			} {
				if err := os.WriteFile(filepath.Join(dir, "docker-credential-"+name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			primary := filepath.Join(dir, "helm.json")
			if err := os.WriteFile(primary, []byte(tc.primary), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.fallback != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.fallback), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HELM_REGISTRY_CONFIG", primary)
			authorizer, err := newRegistryAuthorizer(context.Background(), cli.New())
			if err != nil {
				t.Fatal(err)
			}
			got, err := authorizer.Credential(context.Background(), "registry.test")
			if tc.fail {
				if err == nil || strings.Contains(err.Error(), "sensitive") {
					t.Fatalf("unredacted or missing error: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("credential semantics changed: %#v %v", got, err)
			}
			contents, err := os.ReadFile(primary)
			if err != nil || string(contents) != tc.primary {
				t.Fatal("read-only resolver wrote credential config")
			}
		})
	}
}

func TestOCIDetectsDefaultCredentialHelperOnlyWithoutAuth(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux default helper precedence")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("DOCKER_CONFIG", dir)
	for _, name := range []string{"pass", "secretservice"} {
		if err := os.WriteFile(filepath.Join(dir, "docker-credential-"+name), []byte("#!/bin/sh\nprintf '%s' '{\"Username\":\""+name+"\",\"Secret\":\"secret\"}'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	primary := filepath.Join(dir, "helm.json")
	t.Setenv("HELM_REGISTRY_CONFIG", primary)
	for _, want := range []string{"secretservice", "pass"} {
		if want == "pass" {
			if err := os.WriteFile(filepath.Join(dir, "pass"), []byte("#!/bin/sh\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		a, err := newRegistryAuthorizer(context.Background(), cli.New())
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Credential(context.Background(), "registry.test")
		if err != nil || got.Username != want {
			t.Fatalf("default selection %s: %#v %v", want, got, err)
		}
	}
}
