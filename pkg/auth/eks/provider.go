// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package eks

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"k8s.io/client-go/rest"
)

// tokenTTL is the EKS token lifetime we advertise to the cache. Must match the
// X-Amz-Expires window below — both are captured at the same signedAt instant
// so the cache never returns a token after the signed URL has expired.
const tokenTTL = 14 * time.Minute // STS tokens valid for 15 min; cache 14 min.

// tokenFetchTimeout caps SDK credential lookup + STS presigning. STS presign
// is purely local crypto, but Credentials.Retrieve walks the full provider
// chain (IMDS, env, profile, SSO, web-identity exchange) which can each hang.
const tokenFetchTimeout = 10 * time.Second

// Provider implements auth.AuthProvider for AWS EKS clusters.
type Provider struct {
	ClusterName string
	Region      string
}

// NewProvider creates an EKS auth provider.
func NewProvider(clusterName, region string) *Provider {
	return &Provider{ClusterName: clusterName, Region: region}
}

// ConfigureTransport sets WrapTransport on the rest.Config to inject
// fresh STS presigned tokens on every K8S API request.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{clusterName: p.ClusterName, region: p.Region}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

// tokenSource generates EKS bearer tokens via STS presigned URLs.
type tokenSource struct {
	clusterName string
	region      string
}

// Token generates a presigned STS GetCallerIdentity URL for EKS auth.
//
// signedAt is captured once and used for both PresignHTTP and the cache
// expiry so the advertised TTL is always anchored to the same wall-clock
// instant as the signed URL.
func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
	defer cancel()

	opts := []func(*awsconfig.LoadOptions) error{}
	if s.region != "" {
		opts = append(opts, awsconfig.WithRegion(s.region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	stsURL := fmt.Sprintf(
		"https://sts.%s.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60",
		s.region,
	)
	req, err := http.NewRequest("GET", stsURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create STS request: %w", err)
	}
	req.Header.Set("x-k8s-aws-id", s.clusterName)

	signedAt := time.Now()
	signer := v4.NewSigner()
	signedURL, _, err := signer.PresignHTTP(ctx, creds, req,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"sts", s.region, signedAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to presign STS request: %w", err)
	}

	token := "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(signedURL))
	return token, signedAt.Add(tokenTTL), nil
}

// RegionFromEndpoint extracts the AWS region from an EKS endpoint URL.
// EKS endpoints: https://<id>.<suffix>.<region>.eks.amazonaws.com
func RegionFromEndpoint(endpoint string) string {
	host := strings.TrimPrefix(endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	parts := strings.Split(host, ".")
	if len(parts) >= 5 {
		return parts[len(parts)-4]
	}
	return ""
}
