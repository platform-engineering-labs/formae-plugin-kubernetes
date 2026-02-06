// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Config holds the K8S plugin configuration extracted from target config.
type Config struct {
	// Context is the kubeconfig context to use (optional, uses current-context if empty)
	Context string `json:"context,omitempty"`

	// Namespace is the default namespace for namespaced resources
	Namespace string `json:"namespace,omitempty"`

	// Kubeconfig is the path to kubeconfig file (optional, defaults to ~/.kube/config)
	Kubeconfig string `json:"kubeconfig,omitempty"`
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
func (c *Config) ToK8sConfig() (*rest.Config, error) {
	kubeconfig := c.Kubeconfig
	if kubeconfig == "" {
		// Check KUBECONFIG env var first
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		// Fall back to default location
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	// Build config with optional context override
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

// EffectiveNamespace returns the namespace to use for operations.
// Returns "default" if no namespace is configured.
func (c *Config) EffectiveNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return "default"
}
