// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package gke

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
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
	log := plugin.LoggerFromContext(ctx).With("auth", "GKE")

	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		log.Warn("GCP credentials not found — run `gcloud auth application-default login` "+
			"or set GOOGLE_APPLICATION_CREDENTIALS to a service-account key file",
			"err", err.Error())
		return "", time.Time{}, fmt.Errorf(
			"GCP credentials not found — run `gcloud auth application-default login`, "+
				"or set GOOGLE_APPLICATION_CREDENTIALS to a service-account key file: %w", err)
	}

	tok, err := creds.TokenSource.Token()
	if err != nil {
		if isReauthError(err) {
			log.Warn("GCP reauth required: Workspace org reauth policy returned `invalid_rapt`. "+
				"Re-authenticate with `gcloud auth application-default login`, or switch to a "+
				"service account by setting GOOGLE_APPLICATION_CREDENTIALS — service accounts "+
				"are not subject to reauth policies",
				"err", err.Error())
			return "", time.Time{}, fmt.Errorf(
				"GCP reauth required (org policy `invalid_rapt` expired the session) — "+
					"re-authenticate with `gcloud auth application-default login` or set "+
					"GOOGLE_APPLICATION_CREDENTIALS to a service-account key: %w", err)
		}
		log.Warn("failed to obtain GCP access token", "err", err.Error())
		return "", time.Time{}, fmt.Errorf("failed to get GCP token: %w", err)
	}
	return tok.AccessToken, tok.Expiry, nil
}

// isReauthError detects the `invalid_rapt` / reauth-required OAuth response that
// Google Workspace organizations issue when their reauth policy expires an ADC
// session. These errors are user-resolvable (re-login or switch to a SA) but
// otherwise bubble up from the oauth2 library as opaque wrapped errors.
func isReauthError(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "invalid_rapt") ||
		strings.Contains(m, "reauth related error")
}
