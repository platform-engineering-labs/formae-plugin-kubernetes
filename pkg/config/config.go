// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Config holds the K8S plugin configuration extracted from target config.
// JSON tags match the PKL Config class output fields (uppercase).
type Config struct {
	// Context is the kubeconfig context to use (optional, uses current-context if empty)
	Context string `json:"Context,omitempty"`

	// Namespace is the default namespace for namespaced resources
	Namespace string `json:"Namespace,omitempty"`

	// Kubeconfig is the path to kubeconfig file (optional, defaults to ~/.kube/config)
	Kubeconfig string `json:"Kubeconfig,omitempty"`

	// WaitForLoadBalancer controls whether Service (type LoadBalancer) and Ingress
	// resources report InProgress until a load balancer address is assigned.
	// Defaults to true (production behavior). Set to false for local clusters
	// without a cloud load balancer controller (OrbStack, minikube, kind).
	WaitForLoadBalancer *bool `json:"WaitForLoadBalancer,omitempty"`

	// Endpoint is the API server URL for direct connection (e.g., EKS cluster endpoint).
	// When set, bypasses kubeconfig and connects directly. Requires CertificateAuthority.
	Endpoint string `json:"Endpoint,omitempty"`

	// CertificateAuthority is the base64-encoded CA certificate for the API server.
	// Used with Endpoint for direct cluster authentication.
	CertificateAuthority string `json:"CertificateAuthority,omitempty"`

	// ClusterName is the K8S cluster name. Required for EKS authentication —
	// used in the STS presigned token header. If Endpoint matches *.eks.amazonaws.com
	// and ClusterName is set, the plugin auto-generates an STS bearer token.
	ClusterName string `json:"ClusterName,omitempty"`
}

// FromTargetConfig extracts Config from the target configuration bytes.
func FromTargetConfig(targetConfig []byte) (*Config, error) {
	if len(targetConfig) == 0 {
		return &Config{}, nil
	}

	var cfg Config
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse target config: %w", err)
	}

	return &cfg, nil
}

// ToK8sConfig converts the plugin config to a Kubernetes rest.Config.
// If Endpoint and CertificateAuthority are set, connects directly to the
// API server (e.g., EKS). Otherwise uses kubeconfig file-based auth.
func (c *Config) ToK8sConfig() (*rest.Config, error) {
	// Direct endpoint connection (e.g., EKS cluster provisioned by Formae)
	if c.Endpoint != "" {
		caData, err := base64.StdEncoding.DecodeString(c.CertificateAuthority)
		if err != nil {
			return nil, fmt.Errorf("failed to decode certificate authority: %w", err)
		}

		cfg := &rest.Config{
			Host: c.Endpoint,
			TLSClientConfig: rest.TLSClientConfig{
				CAData: caData,
			},
		}

		// Auto-detect EKS and generate STS token
		if c.isEKS() {
			token, err := c.getEKSToken()
			if err != nil {
				return nil, fmt.Errorf("failed to get EKS token: %w", err)
			}
			cfg.BearerToken = token
		}

		return cfg, nil
	}

	// Kubeconfig-based connection
	kubeconfig := c.Kubeconfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if c.Context != "" {
		configOverrides.CurrentContext = c.Context
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		configOverrides,
	).ClientConfig()

	if err != nil {
		return nil, fmt.Errorf("unable to load kubeconfig: %w", err)
	}

	return config, nil
}

// ShouldWaitForLoadBalancer returns whether to wait for LB address assignment.
// Defaults to true (production behavior) when not explicitly set.
func (c *Config) ShouldWaitForLoadBalancer() bool {
	if c.WaitForLoadBalancer == nil {
		return true
	}
	return *c.WaitForLoadBalancer
}

// EffectiveNamespace returns the namespace to use for operations.
// Returns "default" if no namespace is configured.
func (c *Config) EffectiveNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return "default"
}

