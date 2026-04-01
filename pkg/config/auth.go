// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// isEKS returns true if the endpoint is an EKS API server.
func (c *Config) isEKS() bool {
	return c.ClusterName != "" && strings.Contains(c.Endpoint, ".eks.amazonaws.com")
}

// eksRegion extracts the AWS region from an EKS endpoint URL.
// EKS endpoints follow the pattern: https://<id>.<suffix>.<region>.eks.amazonaws.com
func (c *Config) eksRegion() string {
	// Strip https:// prefix and split by dots
	host := strings.TrimPrefix(c.Endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	parts := strings.Split(host, ".")
	// Format: <id>.<suffix>.<region>.eks.amazonaws.com
	// Region is at index len-4 (before "eks.amazonaws.com")
	if len(parts) >= 5 {
		return parts[len(parts)-4]
	}
	return ""
}

// getEKSToken generates a presigned STS GetCallerIdentity URL for EKS authentication.
// Uses the v4 signer directly to include X-Amz-Expires in the signed URL,
// matching the behavior of `aws eks get-token`.
func (c *Config) getEKSToken() (string, error) {
	ctx := context.Background()

	region := c.eksRegion()
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	// Build the STS GetCallerIdentity request with X-Amz-Expires included
	// before signing, so the expiry is part of the signature.
	stsURL := fmt.Sprintf("https://sts.%s.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60", region)
	req, err := http.NewRequest("GET", stsURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create STS request: %w", err)
	}
	req.Header.Set("x-k8s-aws-id", c.ClusterName)

	// Presign with v4 signer — the empty SHA256 hash is for unsigned payload
	signer := v4.NewSigner()
	signedURL, _, err := signer.PresignHTTP(ctx, creds, req,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"sts", region, time.Now())
	if err != nil {
		return "", fmt.Errorf("failed to presign STS request: %w", err)
	}

	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(signedURL)), nil
}
