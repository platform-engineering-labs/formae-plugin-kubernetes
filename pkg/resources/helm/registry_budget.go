// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"helm.sh/helm/v3/pkg/cli"
	registryauth "oras.land/oras-go/v2/registry/remote/auth"
)

// Mirror Helm's configured store and Docker fallback, while binding credential
// helper execution as well as HTTP to the chart-owning callback lifetime.
func newRegistryAuthorizer(ctx context.Context, settings *cli.EnvSettings) (registryauth.Client, error) {
	lookup, err := registryCredentialLookup(settings.RegistryConfig)
	if err != nil {
		return registryauth.Client{}, err
	}
	bounded := func(request context.Context, host string) (registryauth.Credential, error) {
		live, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(request, cancel)
		defer stop()
		if err := request.Err(); err != nil {
			return registryauth.Credential{}, err
		}
		if err := live.Err(); err != nil {
			return registryauth.Credential{}, err
		}
		return lookup(live, host)
	}
	return registryauth.Client{Client: &http.Client{Timeout: 30 * time.Second, Transport: auth.WithOperationContext(http.DefaultTransport, ctx)}, Cache: registryauth.NewCache(), Credential: bounded}, nil
}
