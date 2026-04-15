// (C) 2025 Platform Engineering Labs Inc.
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

const azureK8SScope = "6dae42f8-4368-4678-94ff-3960e28e3630/.default"

// Provider implements auth.AuthProvider for Azure AKS clusters.
type Provider struct {
	ResourceGroup string
	ClusterName   string
}

// NewProvider creates an AKS auth provider.
func NewProvider(resourceGroup, clusterName string) *Provider {
	return &Provider{ResourceGroup: resourceGroup, ClusterName: clusterName}
}

// ConfigureTransport sets WrapTransport to inject Azure AD tokens.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct{}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create Azure credential: %w", err)
	}
	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{azureK8SScope},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get Azure token: %w", err)
	}
	return tok.Token, tok.ExpiresOn, nil
}
