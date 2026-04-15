// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package ovh

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	ovhclient "github.com/ovh/go-ovh/ovh"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Provider implements auth.AuthProvider for OVH Managed K8S clusters.
type Provider struct {
	ServiceName string
	ClusterID   string
}

// NewProvider creates an OVH auth provider.
func NewProvider(serviceName, clusterID string) *Provider {
	return &Provider{ServiceName: serviceName, ClusterID: clusterID}
}

// ConfigureTransport fetches a kubeconfig from OVH API and configures
// the rest.Config with the embedded credentials.
func (p *Provider) ConfigureTransport(cfg *rest.Config) error {
	source := &tokenSource{serviceName: p.ServiceName, clusterID: p.ClusterID}
	cached := auth.NewCachedTokenSource(source)
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		return auth.NewTokenTransport(rt, cached)
	}
	return nil
}

type tokenSource struct {
	serviceName string
	clusterID   string
}

type kubeconfigResponse struct {
	Content string `json:"content"`
}

func (s *tokenSource) Token(ctx context.Context) (string, time.Time, error) {
	client, err := ovhclient.NewDefaultClient()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to create OVH client: %w", err)
	}

	var resp kubeconfigResponse
	endpoint := fmt.Sprintf("/cloud/project/%s/kube/%s/kubeconfig", s.serviceName, s.clusterID)
	if err := client.PostWithContext(ctx, endpoint, nil, &resp); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to get OVH kubeconfig: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		// Content might not be base64 encoded
		decoded = []byte(resp.Content)
	}

	kc, err := clientcmd.RESTConfigFromKubeConfig(decoded)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to parse OVH kubeconfig: %w", err)
	}

	// OVH kubeconfigs embed a bearer token
	if kc.BearerToken != "" {
		// Conservative 30-minute TTL since OVH doesn't expose expiry
		return kc.BearerToken, time.Now().Add(30 * time.Minute), nil
	}

	return "", time.Time{}, fmt.Errorf("OVH kubeconfig does not contain a bearer token")
}
