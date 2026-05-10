// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/aks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/gke"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/oci"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/ovh"
)

// Config holds the K8S plugin configuration extracted from target config.
type Config struct {
	Auth json.RawMessage `json:"Auth"`

	// KubernetesVersion is an optional override for the cluster's reported
	// version, in MAJOR.MINOR form (e.g., "1.32"). When unset, the plugin
	// auto-detects via Discovery().ServerVersion(). The override is consumed
	// by the @K8sVersion field-gate preflight check; it does not change which
	// API endpoints are called. Useful for dry-run, offline planning, or
	// pinning to a lower version for portability.
	KubernetesVersion string `json:"KubernetesVersion,omitempty"`

	// Parsed auth config — populated by FromTargetConfig
	authType string
	authRaw  json.RawMessage
}

// authHeader is used to extract just the Type discriminator.
type authHeader struct {
	Type string `json:"Type"`
}

// KubeconfigAuthConfig holds kubeconfig-based auth fields.
type KubeconfigAuthConfig struct {
	Context    string `json:"Context,omitempty"`
	Kubeconfig string `json:"Kubeconfig,omitempty"`
}

// CloudAuthConfig holds fields common to all cloud auth types.
type CloudAuthConfig struct {
	Endpoint             string `json:"Endpoint"`
	CertificateAuthority string `json:"CertificateAuthority"`
}

// EKSAuthConfig holds EKS-specific auth fields.
type EKSAuthConfig struct {
	CloudAuthConfig
	ClusterName string `json:"ClusterName"`
	Region      string `json:"Region,omitempty"`
}

// AKSAuthConfig holds AKS-specific auth fields.
type AKSAuthConfig struct {
	CloudAuthConfig
	ResourceGroup string `json:"ResourceGroup,omitempty"`
	ClusterName   string `json:"ClusterName,omitempty"`
}

// OVHAuthConfig holds OVH-specific auth fields.
type OVHAuthConfig struct {
	CloudAuthConfig
	ServiceName string `json:"ServiceName"`
	ClusterID   string `json:"ClusterId"`
}

// OCIAuthConfig holds OCI-specific auth fields.
type OCIAuthConfig struct {
	CloudAuthConfig
	ClusterOCID string `json:"ClusterOcid"`
	Region      string `json:"Region,omitempty"`
}

// FromTargetConfig extracts Config from the target configuration bytes.
func FromTargetConfig(targetConfig []byte) (*Config, error) {
	if len(targetConfig) == 0 {
		return nil, fmt.Errorf("empty target config")
	}

	var cfg Config
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse target config: %w", err)
	}

	var header authHeader
	if err := json.Unmarshal(cfg.Auth, &header); err != nil {
		return nil, fmt.Errorf("failed to parse auth type: %w", err)
	}
	cfg.authType = header.Type
	cfg.authRaw = cfg.Auth

	return &cfg, nil
}

// AuthType returns the auth strategy type string.
func (c *Config) AuthType() string {
	return c.authType
}

// ToK8sConfig builds a rest.Config based on the auth strategy.
func (c *Config) ToK8sConfig() (*rest.Config, error) {
	switch c.authType {
	case "Kubeconfig":
		return c.buildKubeconfigConfig()
	case "EKS":
		return c.buildCloudConfig(c.newEKSProvider)
	case "GKE":
		return c.buildCloudConfig(c.newGKEProvider)
	case "AKS":
		return c.buildCloudConfig(c.newAKSProvider)
	case "OVH":
		return c.buildCloudConfig(c.newOVHProvider)
	case "OCI":
		return c.buildCloudConfig(c.newOCIProvider)
	default:
		return nil, fmt.Errorf("unsupported auth type: %s", c.authType)
	}
}

func (c *Config) buildKubeconfigConfig() (*rest.Config, error) {
	var kc KubeconfigAuthConfig
	if err := json.Unmarshal(c.authRaw, &kc); err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig auth: %w", err)
	}

	kubeconfig := kc.Kubeconfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kc.Context != "" {
		overrides.CurrentContext = kc.Context
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	// Bound every request; matches the buildCloudConfig safety net.
	cfg.Timeout = 30 * time.Second
	return cfg, nil
}

func (c *Config) buildCloudConfig(providerFn func() (auth.AuthProvider, *CloudAuthConfig, error)) (*rest.Config, error) {
	provider, cloud, err := providerFn()
	if err != nil {
		return nil, err
	}

	caData, err := base64.StdEncoding.DecodeString(cloud.CertificateAuthority)
	if err != nil {
		return nil, fmt.Errorf("failed to decode certificate authority: %w", err)
	}

	cfg := &rest.Config{
		Host: cloud.Endpoint,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
		// Bound every request; without this, a silent apiserver or slow
		// token refresh can wedge a CRUD call until formae's 40s-per-retry
		// state-machine timeout elapses 11× and fails with
		// PluginOperatorMissingInAction.
		Timeout: 30 * time.Second,
	}

	// Suppress K8S API deprecation warnings
	cfg.WarningHandler = rest.NoWarnings{}

	if err := provider.ConfigureTransport(cfg); err != nil {
		return nil, fmt.Errorf("failed to configure auth transport: %w", err)
	}

	return cfg, nil
}

func (c *Config) newEKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac EKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse EKS auth config: %w", err)
	}
	region := ac.Region
	if region == "" {
		region = eks.RegionFromEndpoint(ac.Endpoint)
	}
	return eks.NewProvider(ac.ClusterName, region), &ac.CloudAuthConfig, nil
}

func (c *Config) newGKEProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac CloudAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse GKE auth config: %w", err)
	}
	return gke.NewProvider(), &ac, nil
}

func (c *Config) newAKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac AKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse AKS auth config: %w", err)
	}
	return aks.NewProvider(ac.ResourceGroup, ac.ClusterName), &ac.CloudAuthConfig, nil
}

func (c *Config) newOVHProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OVHAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OVH auth config: %w", err)
	}
	return ovh.NewProvider(ac.ServiceName, ac.ClusterID), &ac.CloudAuthConfig, nil
}

func (c *Config) newOCIProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OCIAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OCI auth config: %w", err)
	}
	return oci.NewProvider(ac.ClusterOCID, ac.Region), &ac.CloudAuthConfig, nil
}
