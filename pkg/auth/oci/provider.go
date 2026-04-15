// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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

// ConfigureTransport fetches cluster credentials from OCI and configures
// the rest.Config.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{clusterOCID: p.ClusterOCID, region: p.Region}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct {
	clusterOCID string
	region      string
}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	client, err := containerengine.NewContainerEngineClientWithConfigurationProvider(
		common.DefaultConfigProvider(),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create OKE client: %w", err)
	}

	if s.region != "" {
		client.SetRegion(s.region)
	}

	resp, err := client.CreateKubeconfig(ctx, containerengine.CreateKubeconfigRequest{
		ClusterId: &s.clusterOCID,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get OKE kubeconfig: %w", err)
	}
	defer func() { _ = resp.Content.Close() }()

	content, err := io.ReadAll(resp.Content)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to read OKE kubeconfig response: %w", err)
	}

	kc, err := clientcmd.RESTConfigFromKubeConfig(content)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse OKE kubeconfig: %w", err)
	}

	if kc.BearerToken != "" {
		return kc.BearerToken, time.Now().Add(30 * time.Minute), nil
	}

	return "", time.Time{}, fmt.Errorf("OKE kubeconfig does not contain a bearer token")
}
