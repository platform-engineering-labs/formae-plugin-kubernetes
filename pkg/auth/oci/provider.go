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
func (s *tokenSource) Token(_ context.Context) (string, time.Time, error) {
	provider := common.DefaultConfigProvider()

	region := s.region
	if region == "" {
		r, err := provider.Region()
		if err != nil {
			return "", time.Time{}, fmt.Errorf("failed to determine OCI region: %w", err)
		}
		region = r
	}

	baseURL := tokenURL(region, s.clusterOCID)

	req, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create signing request: %w", err)
	}

	now := time.Now().UTC()
	req.Header.Set("date", now.Format(http.TimeFormat))
	req.Header.Set("host", req.URL.Host)

	signer := common.DefaultRequestSigner(provider)
	if err := signer.Sign(req); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign OKE token request: %w", err)
	}

	token := buildPresignedToken(baseURL, req.Header.Get("authorization"), req.Header.Get("date"))
	expiry := now.Add(4 * time.Minute)

	return token, expiry, nil
}
