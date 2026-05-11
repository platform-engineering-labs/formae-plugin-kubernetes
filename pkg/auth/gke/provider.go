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

// tokenFetchTimeout caps ADC lookup + token mint. google.FindDefaultCredentials
// reads env vars and well-known files; TokenSource.Token then hits the OAuth2
// endpoint over the network. Either can hang on misconfigured agents.
const tokenFetchTimeout = 10 * time.Second

// Provider implements auth.AuthProvider for Google GKE clusters.
//
// ProjectID + Location + ClusterName uniquely identify a cluster within
// Google Cloud; they are part of the cache key in pkg/transport so two
// targets pointing at different GCP projects/clusters never alias on the
// same CachedTokenSource. The fields are not (yet) used to scope the OAuth
// token itself — `cloud-platform` scope is universal — but reserving them on
// the struct lets us add per-cluster token sources later (e.g. impersonated
// service accounts) without changing call sites.
type Provider struct {
	ProjectID   string
	Location    string
	ClusterName string
}

// NewProvider creates a GKE auth provider.
func NewProvider(projectID, location, clusterName string) *Provider {
	return &Provider{
		ProjectID:   projectID,
		Location:    location,
		ClusterName: clusterName,
	}
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
	ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
	defer cancel()

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

	// creds.TokenSource.Token() has no context parameter, so we enforce
	// the timeout by racing the call against ctx.Done(). On timeout the
	// goroutine leaks until the inner oauth2 HTTP client's own timeout
	// trips — acceptable since the goroutine is doing bounded network I/O,
	// not holding plugin-level locks.
	type result struct {
		access string
		expiry time.Time
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		t, err := creds.TokenSource.Token()
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{access: t.AccessToken, expiry: t.Expiry}
	}()

	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		log.Warn("GCP token mint timed out", "err", ctx.Err().Error())
		return "", time.Time{}, fmt.Errorf("GCP token mint timed out: %w", ctx.Err())
	}
	if r.err != nil {
		if isReauthError(r.err) {
			log.Warn("GCP reauth required: Workspace org reauth policy returned `invalid_rapt`. "+
				"Re-authenticate with `gcloud auth application-default login`, or switch to a "+
				"service account by setting GOOGLE_APPLICATION_CREDENTIALS — service accounts "+
				"are not subject to reauth policies",
				"err", r.err.Error())
			return "", time.Time{}, fmt.Errorf(
				"GCP reauth required (org policy `invalid_rapt` expired the session) — "+
					"re-authenticate with `gcloud auth application-default login` or set "+
					"GOOGLE_APPLICATION_CREDENTIALS to a service-account key: %w", r.err)
		}
		log.Warn("failed to obtain GCP access token", "err", r.err.Error())
		return "", time.Time{}, fmt.Errorf("failed to get GCP token: %w", r.err)
	}
	return r.access, r.expiry, nil
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
