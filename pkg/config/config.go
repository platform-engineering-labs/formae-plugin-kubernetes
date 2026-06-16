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

	// CustomResourceDiscovery selects how custom resources (instances of CRDs)
	// participate in discovery (List for K8S::Custom::Resource):
	//
	//   "none"   — no custom-resource discovery (default). A fresh cluster does
	//              not pull operator-internal CRs into inventory.
	//   "groups" — discover only CRs whose API group is in CustomResourceGroups.
	//   "all"    — discover instances of every installed CRD.
	//
	// CRUD of explicitly-declared custom resources is unaffected by this field.
	// For backward compatibility, an empty value with a non-empty
	// CustomResourceGroups is treated as "groups".
	CustomResourceDiscovery string `json:"CustomResourceDiscovery,omitempty"`

	// CustomResourceGroups is the API-group allowlist consulted when
	// CustomResourceDiscovery is "groups".
	CustomResourceGroups []string `json:"CustomResourceGroups,omitempty"`

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

// ResolvedString unmarshals either a plain JSON string or a formae
// resolvable envelope ({"$ref":..., "$value":"..."} or
// {"$res":true, "$value":"..."}) into a flat string. Targets created
// from a Forma's $ref reach plugins with the envelope shape; the K8s
// plugin's auth parsing previously expected the bare string and failed
// to construct a client when the envelope leaked through.
type ResolvedString string

// UnmarshalJSON accepts either a quoted string or an object with a
// "$value" key (the formae resolvable envelope).
func (rs *ResolvedString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		*rs = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*rs = ResolvedString(s)
		return nil
	}
	if data[0] == '{' {
		var envelope struct {
			Value string `json:"$value"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		*rs = ResolvedString(envelope.Value)
		return nil
	}
	return fmt.Errorf("ResolvedString: unsupported JSON shape: %s", string(data))
}

// String returns the resolved value as a plain string.
func (rs ResolvedString) String() string { return string(rs) }

// CloudAuthConfig holds fields common to all cloud auth types. Endpoint
// and CertificateAuthority are ResolvedString so they accept both literal
// strings and formae resolvable envelopes.
type CloudAuthConfig struct {
	Endpoint             ResolvedString `json:"Endpoint"`
	CertificateAuthority ResolvedString `json:"CertificateAuthority"`
}

// EKSAuthConfig holds EKS-specific auth fields.
type EKSAuthConfig struct {
	CloudAuthConfig
	ClusterName string `json:"ClusterName"`
	Region      string `json:"Region,omitempty"`
}

// AKSAuthConfig holds AKS-specific auth fields. Scope is optional and
// overrides the default AKS-managed-AAD app for private clusters with a
// custom AAD integration.
type AKSAuthConfig struct {
	CloudAuthConfig
	ResourceGroup string `json:"ResourceGroup,omitempty"`
	ClusterName   string `json:"ClusterName,omitempty"`
	Scope         string `json:"Scope,omitempty"`
}

// GKEAuthConfig holds GKE-specific auth fields. ProjectID, Location, and
// ClusterName uniquely identify a GKE cluster within Google Cloud and are
// required for cache keying — without them, two targets pointing at
// different projects/clusters would alias on the same cached token.
type GKEAuthConfig struct {
	CloudAuthConfig
	ProjectID   string `json:"ProjectId,omitempty"`
	Location    string `json:"Location,omitempty"`
	ClusterName string `json:"ClusterName,omitempty"`
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

	// json.Unmarshal happily leaves cfg.Auth as nil / `null` when the field
	// is missing or explicitly null. Forwarding that to json.Unmarshal again
	// produces a confusing "unexpected end of JSON input" error — surface
	// the actual cause instead.
	if len(cfg.Auth) == 0 || string(cfg.Auth) == "null" {
		return nil, fmt.Errorf("target config missing required Auth block")
	}

	var header authHeader
	if err := json.Unmarshal(cfg.Auth, &header); err != nil {
		return nil, fmt.Errorf("failed to parse auth type: %w", err)
	}
	if header.Type == "" {
		return nil, fmt.Errorf("target config Auth block missing required Type field")
	}
	cfg.authType = header.Type
	cfg.authRaw = cfg.Auth

	return &cfg, nil
}

// AuthType returns the auth strategy type string.
func (c *Config) AuthType() string {
	return c.authType
}

// CacheKey returns a stable identity string for the cluster this Config
// targets. The transport-layer cache uses this key to dedupe *transport.Client
// (and the underlying CachedTokenSource) across CRUD calls — without it,
// every Create/Read/Update/Delete/Status/List would rebuild the client and
// re-mint a token.
//
// Composition (auth-type-specific):
//
//	Kubeconfig: "Kubeconfig|<kubeconfig-path>|<context>"
//	EKS:        "EKS|<endpoint>|<cluster-name>|<region>"
//	GKE:        "GKE|<endpoint>|<project>|<location>|<cluster-name>"
//	AKS:        "AKS|<endpoint>|<resource-group>|<cluster-name>|<scope>"
//	OVH:        "OVH|<endpoint>|<service-name>|<cluster-id>"
//	OCI:        "OCI|<endpoint>|<cluster-ocid>|<region>"
//
// Endpoint is included as a defense-in-depth tiebreaker in case a user
// duplicates a logical identifier (e.g. same cluster name across regions
// they forgot to set). For Kubeconfig auth the path+context are enough.
//
// CacheKey returns ("", error) on malformed Auth blocks; the caller should
// treat that as a hard cache miss and surface the error.
func (c *Config) CacheKey() (string, error) {
	switch c.authType {
	case "Kubeconfig":
		var kc KubeconfigAuthConfig
		if err := json.Unmarshal(c.authRaw, &kc); err != nil {
			return "", fmt.Errorf("CacheKey: parse Kubeconfig auth: %w", err)
		}
		// We deliberately do NOT resolve $KUBECONFIG / $HOME here — the same
		// resolution happens inside buildKubeconfigConfig and is allowed to
		// vary by process environment. Two targets that both omit
		// Kubeconfig share a key, which is correct: they resolve to the
		// same kubeconfig.
		return fmt.Sprintf("Kubeconfig|%s|%s", kc.Kubeconfig, kc.Context), nil
	case "EKS":
		var ac EKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &ac); err != nil {
			return "", fmt.Errorf("CacheKey: parse EKS auth: %w", err)
		}
		return fmt.Sprintf("EKS|%s|%s|%s", ac.Endpoint, ac.ClusterName, ac.Region), nil
	case "GKE":
		var ac GKEAuthConfig
		if err := json.Unmarshal(c.authRaw, &ac); err != nil {
			return "", fmt.Errorf("CacheKey: parse GKE auth: %w", err)
		}
		return fmt.Sprintf("GKE|%s|%s|%s|%s", ac.Endpoint, ac.ProjectID, ac.Location, ac.ClusterName), nil
	case "AKS":
		var ac AKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &ac); err != nil {
			return "", fmt.Errorf("CacheKey: parse AKS auth: %w", err)
		}
		return fmt.Sprintf("AKS|%s|%s|%s|%s", ac.Endpoint, ac.ResourceGroup, ac.ClusterName, ac.Scope), nil
	case "OVH":
		var ac OVHAuthConfig
		if err := json.Unmarshal(c.authRaw, &ac); err != nil {
			return "", fmt.Errorf("CacheKey: parse OVH auth: %w", err)
		}
		return fmt.Sprintf("OVH|%s|%s|%s", ac.Endpoint, ac.ServiceName, ac.ClusterID), nil
	case "OCI":
		var ac OCIAuthConfig
		if err := json.Unmarshal(c.authRaw, &ac); err != nil {
			return "", fmt.Errorf("CacheKey: parse OCI auth: %w", err)
		}
		return fmt.Sprintf("OCI|%s|%s|%s", ac.Endpoint, ac.ClusterOCID, ac.Region), nil
	default:
		return "", fmt.Errorf("CacheKey: unsupported auth type: %s", c.authType)
	}
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
	// All three sources empty (no Auth.Kubeconfig, no $KUBECONFIG, no $HOME)
	// would silently fall through to clientcmd's in-cluster auth path, which
	// fails on dev machines with the cryptic "no Auth Provider found for
	// name" error. Bail early with an actionable message — supporting
	// in-cluster auth (e.g. when running the plugin from inside a pod)
	// should be a deliberate, explicit opt-in.
	if kubeconfig == "" {
		return nil, fmt.Errorf("no kubeconfig: set Auth.Kubeconfig, $KUBECONFIG, or $HOME")
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

	caData, err := base64.StdEncoding.DecodeString(string(cloud.CertificateAuthority))
	if err != nil {
		return nil, fmt.Errorf("failed to decode certificate authority: %w", err)
	}

	cfg := &rest.Config{
		Host: string(cloud.Endpoint),
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
		region = eks.RegionFromEndpoint(string(ac.Endpoint))
	}
	return eks.NewProvider(ac.ClusterName, region), &ac.CloudAuthConfig, nil
}

func (c *Config) newGKEProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac GKEAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse GKE auth config: %w", err)
	}
	return gke.NewProvider(ac.ProjectID, ac.Location, ac.ClusterName), &ac.CloudAuthConfig, nil
}

func (c *Config) newAKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac AKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse AKS auth config: %w", err)
	}
	return aks.NewProvider(ac.ResourceGroup, ac.ClusterName, ac.Scope), &ac.CloudAuthConfig, nil
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
