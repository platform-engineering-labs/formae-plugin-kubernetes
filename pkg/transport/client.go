// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client wraps the Kubernetes clientset with plugin configuration.
type Client struct {
	*kubernetes.Clientset
	Dynamic dynamic.Interface
	Config  *config.Config
}

// NewClient creates a new Kubernetes client from the provided config.
func NewClient(cfg *config.Config) (*Client, error) {
	restConfig, err := cfg.ToK8sConfig()
	if err != nil {
		return nil, err
	}

	// Suppress K8S API deprecation warnings (e.g., Endpoints deprecated in v1.33+)
	// that would otherwise be logged to stderr and treated as plugin errors by Formae.
	restConfig.WarningHandler = rest.NoWarnings{}

	// Disable client-go's local QPS/Burst throttle. Formae core already gates
	// plugin invocations via Plugin.RateLimit(); a second token bucket inside
	// client-go just adds redundant client-side delays and emits klog INFO
	// lines ("Waited before sending request") that Formae mis-classifies as
	// plugin errors. Apiserver-side APF (429 + Retry-After) remains the
	// authoritative back-pressure mechanism.
	restConfig.QPS = -1
	restConfig.Burst = -1

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &Client{
		Clientset: clientset,
		Dynamic:   dynamicClient,
		Config:    cfg,
	}, nil
}
