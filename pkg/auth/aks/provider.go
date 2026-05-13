// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package aks

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"k8s.io/client-go/rest"
)

// DefaultAKSScope is the Azure-managed AAD app ID for AKS clusters. Private
// clusters integrated with a customer-managed AAD app expose a different
// scope — Provider.Scope overrides this default.
const DefaultAKSScope = "6dae42f8-4368-4678-94ff-3960e28e3630/.default"

// tokenFetchTimeout caps the full Azure credential chain walk plus the
// access-token mint. NewDefaultAzureCredential probes IMDS, env vars, and
// optional CLI/PowerShell auth — any can hang on misconfigured agents.
const tokenFetchTimeout = 10 * time.Second

// Provider implements auth.AuthProvider for Azure AKS clusters.
//
// ResourceGroup + ClusterName uniquely identify a cluster within a
// subscription; they are part of the cache key in pkg/transport so two
// targets pointing at different AKS clusters never alias on the same
// CachedTokenSource. Scope is optional and overrides DefaultAKSScope for
// clusters running a custom AAD integration.
type Provider struct {
	ResourceGroup string
	ClusterName   string
	Scope         string
}

// NewProvider creates an AKS auth provider. Scope defaults to DefaultAKSScope
// when empty.
func NewProvider(resourceGroup, clusterName, scope string) *Provider {
	return &Provider{
		ResourceGroup: resourceGroup,
		ClusterName:   clusterName,
		Scope:         scope,
	}
}

// ConfigureTransport sets WrapTransport to inject Azure AD tokens.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	scope := p.Scope
	if scope == "" {
		scope = DefaultAKSScope
	}
	source := &tokenSource{scope: scope}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct {
	scope string
}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
	defer cancel()

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create Azure credential: %w", err)
	}
	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{s.scope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get Azure token: %w", err)
	}
	return tok.Token, tok.ExpiresOn, nil
}
