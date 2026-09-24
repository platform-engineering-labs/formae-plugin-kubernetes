// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/aks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/gke"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/oidc"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"k8s.io/client-go/rest"
)

type operationKey struct{}

// NewOperationContext owns a callback lifetime. This marker grants no broker
// authority: the SDK source still resolves authority exclusively from ctx.
func NewOperationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	return context.WithValue(ctx, operationKey{}, ctx.Done()), cancel
}

// CheckOperation rejects context-free or already-finished OIDC requests even
// when a reusable bearer cache contains a valid token.
func CheckOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done, ok := ctx.Value(operationKey{}).(<-chan struct{})
	if !ok || done == nil {
		return plugin.ErrNoOidcBroker
	}
	select {
	case <-done:
		return context.Canceled
	default:
		return nil
	}
}

type operationTokens struct{ cache *auth.CachedTokenSource }

func (s *operationTokens) Token(ctx context.Context) (string, time.Time, error) {
	if err := CheckOperation(ctx); err != nil {
		return "", time.Time{}, err
	}
	service, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.cache.Token(service)
}
func (s *operationTokens) Invalidate() { s.cache.Invalidate() }

func (c *Config) buildOidcConfig() (*rest.Config, error) {
	rc, _, err := c.OidcClientConfig()
	return rc, err
}

// OidcClientConfig builds a context-free client and its shared callback-only
// token source. ClientCache owns its identity/binding isolation; no ctx is stored.
func (c *Config) OidcClientConfig() (*rest.Config, auth.TokenSource, error) {
	if err := c.ValidateAuthPolicy(c.deps.AllowedAuthMethods); err != nil {
		return nil, nil, err
	}
	if !c.UsesOidc() {
		return nil, nil, errors.New("OIDC client requires explicit OIDC")
	}
	rc, source, err := c.oidcParts()
	if err != nil {
		return nil, nil, err
	}
	live := &operationTokens{cache: auth.NewCachedTokenSource(source)}
	attachOidcTokens(rc, live)
	return rc, live, nil
}

// OidcWorkerConfig builds a queue-only worker configuration and returns the
// separate live-callback source for synchronous prefetch/service. Neither result
// retains ctx. The caller must not give the live source to a detached worker.
func (c *Config) OidcWorkerConfig(ctx context.Context, worker auth.TokenSource) (*rest.Config, auth.TokenSource, error) {
	if err := c.CheckRuntime(ctx); err != nil {
		return nil, nil, err
	}
	if !c.UsesOidc() || worker == nil {
		return nil, nil, errors.New("OIDC worker requires explicit OIDC and a queue token source")
	}
	rc, source, err := c.oidcParts()
	if err != nil {
		return nil, nil, err
	}
	attachOidcTokens(rc, worker)
	return rc, &operationTokens{cache: auth.NewCachedTokenSource(source)}, nil
}
func attachOidcTokens(rc *rest.Config, source auth.TokenSource) {
	origin, _ := url.Parse(rc.Host)
	rc.WrapTransport = func(base http.RoundTripper) http.RoundTripper {
		return auth.NewOriginTokenTransport(base, source, origin)
	}
}
func (c *Config) oidcParts() (*rest.Config, auth.TokenSource, error) {
	if c.deps.OidcSource == nil {
		return nil, nil, plugin.ErrNoOidcBroker
	}
	var cloud CloudAuthConfig
	if err := json.Unmarshal(c.authRaw, &cloud); err != nil {
		return nil, nil, err
	}
	var source auth.TokenSource
	switch c.authType {
	case "Oidc":
		var a OidcAuthConfig
		_ = json.Unmarshal(c.authRaw, &a)
		source = oidc.NewProvider(c.deps.OidcSource, a.Audience)
	case "EKS":
		var a EKSAuthConfig
		_ = json.Unmarshal(c.authRaw, &a)
		var identity AwsOidcCredentials
		_ = json.Unmarshal(a.Credentials, &identity)
		source = eks.NewOidcTokenSource(c.deps.OidcSource, identity.RoleARN, a.Region, a.ClusterName)
	case "AKS":
		var a AKSAuthConfig
		_ = json.Unmarshal(c.authRaw, &a)
		var identity AzureOidcCredentials
		_ = json.Unmarshal(a.Credentials, &identity)
		source = aks.NewOidcTokenSource(c.deps.OidcSource, identity.TenantID, identity.ClientID)
	case "GKE":
		var a GKEAuthConfig
		_ = json.Unmarshal(c.authRaw, &a)
		var identity GcpOidcCredentials
		_ = json.Unmarshal(a.Credentials, &identity)
		source = gke.NewOidcTokenSource(c.deps.OidcSource, identity.WorkloadIdentityProvider, identity.ServiceAccountEmail)
	}
	ca, err := base64.StdEncoding.DecodeString(cloud.CertificateAuthority)
	if err != nil {
		return nil, nil, err
	}
	return &rest.Config{Host: cloud.Endpoint, TLSClientConfig: rest.TLSClientConfig{CAData: ca}, Timeout: 30 * time.Second}, source, nil
}

// CheckRuntime runs before cached client or discovery access.
func (c *Config) CheckRuntime(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ValidateAuthPolicy(c.deps.AllowedAuthMethods); err != nil {
		return err
	}
	if c.UsesOidc() {
		if c.deps.OidcSource == nil {
			return plugin.ErrNoOidcBroker
		}
		return CheckOperation(ctx)
	}
	return nil
}
