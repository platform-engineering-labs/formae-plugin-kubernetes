// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package gke

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/rest"
)

// Provider implements auth.AuthProvider for Google GKE clusters.
type Provider struct{}

// NewProvider creates a GKE auth provider.
func NewProvider() *Provider {
	return &Provider{}
}

// ConfigureTransport sets WrapTransport to inject GCP OAuth2 access tokens.
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
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to find GCP credentials: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get GCP token: %w", err)
	}
	return tok.AccessToken, tok.Expiry, nil
}
