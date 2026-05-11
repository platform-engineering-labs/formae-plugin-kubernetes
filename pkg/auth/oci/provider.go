// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"k8s.io/client-go/rest"
)

// tokenTTL matches the OKE token webhook's documented 5-minute lifetime.
// The cache's clock-skew floor (60s) keeps us from serving an expired token.
const tokenTTL = 5 * time.Minute

// tokenFetchTimeout caps OCI config reads + RSA signing. The config provider
// reads `~/.oci/config` and may unlock an encrypted private key — both can
// hang on slow disks or HSM-backed key stores.
const tokenFetchTimeout = 10 * time.Second

// Provider implements auth.AuthProvider for Oracle OKE clusters.
type Provider struct {
	ClusterOCID string
	Region      string
}

// NewProvider creates an OCI auth provider.
func NewProvider(clusterOCID, region string) *Provider {
	return &Provider{ClusterOCID: clusterOCID, Region: region}
}

// ConfigureTransport configures the rest.Config to inject OCI-signed bearer
// tokens for OKE cluster authentication.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{clusterOCID: p.ClusterOCID, region: p.Region}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

// tokenURL builds the OKE cluster request URL used as the signing target.
func tokenURL(region, clusterOCID string) string {
	return fmt.Sprintf("https://containerengine.%s.oraclecloud.com/cluster_request/%s",
		region, clusterOCID)
}

// buildPresignedToken creates a base64-encoded presigned URL token from the
// signed authorization header and date. This matches the token format that
// OKE's Kubernetes token webhook expects.
func buildPresignedToken(baseURL, authorization, date string) string {
	presigned := fmt.Sprintf("%s?authorization=%s&date=%s",
		baseURL, url.QueryEscape(authorization), url.QueryEscape(date))
	return base64.StdEncoding.EncodeToString([]byte(presigned))
}

type tokenSource struct {
	clusterOCID string
	region      string
}

// Token generates a short-lived bearer token by creating a presigned OCI API
// request. This is equivalent to what `oci ce cluster generate-token` does.
//
// signedAt is captured once and used for both the Date header and the cache
// expiry so the advertised TTL is anchored to the same wall-clock instant
// the OKE webhook will validate against.
func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
	defer cancel()

	// OCI's signer and config provider are fully synchronous and do not
	// accept a context, so a wall-clock timeout via context.WithTimeout
	// would otherwise be advisory only. Run the synchronous body in a
	// goroutine and race against ctx.Done() so a stuck file read or HSM
	// signature never wedges the cache. The goroutine leaks until the
	// SDK returns; that's acceptable because the work is all local I/O
	// or local crypto, bounded by the OS.
	type result struct {
		token  string
		expiry time.Time
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		tok, exp, err := s.sign()
		ch <- result{token: tok, expiry: exp, err: err}
	}()
	select {
	case r := <-ch:
		return r.token, r.expiry, r.err
	case <-ctx.Done():
		return "", time.Time{}, fmt.Errorf("OCI token mint timed out: %w", ctx.Err())
	}
}

// sign is the synchronous body of Token. Split out so Token can race it
// against a context deadline; sign itself never observes the context.
func (s *tokenSource) sign() (token string, expiry time.Time, err error) {
	provider := common.DefaultConfigProvider()

	region := s.region
	if region == "" {
		r, rerr := provider.Region()
		if rerr != nil {
			return "", time.Time{}, fmt.Errorf("failed to determine OCI region: %w", rerr)
		}
		region = r
	}

	baseURL := tokenURL(region, s.clusterOCID)

	req, rerr := http.NewRequest(http.MethodGet, baseURL, nil)
	if rerr != nil {
		return "", time.Time{}, fmt.Errorf("failed to create signing request: %w", rerr)
	}

	signedAt := time.Now().UTC()
	req.Header.Set("date", signedAt.Format(http.TimeFormat))
	req.Header.Set("host", req.URL.Host)

	signer := common.DefaultRequestSigner(provider)
	if serr := signer.Sign(req); serr != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign OKE token request: %w", serr)
	}

	tok := buildPresignedToken(baseURL, req.Header.Get("authorization"), req.Header.Get("date"))
	return tok, signedAt.Add(tokenTTL), nil
}
