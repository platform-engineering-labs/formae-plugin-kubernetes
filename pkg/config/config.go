// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	deps     AuthDependencies
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
//
// Every field here is a plain string. formae flattens a resolved reference
// to its scalar before the config reaches a plugin
// (resolver.ConvertToPluginFormat), so an envelope arriving here means the
// reference did NOT resolve — see guardNoUnflattenedReferences.
type CloudAuthConfig struct {
	Endpoint             string `json:"Endpoint"`
	CertificateAuthority string `json:"CertificateAuthority"`
}

// EKSAuthConfig holds EKS-specific auth fields.
type EKSAuthConfig struct {
	CloudAuthConfig
	ClusterName string          `json:"ClusterName"`
	Region      string          `json:"Region,omitempty"`
	Profile     string          `json:"Profile,omitempty"`
	Credentials json.RawMessage `json:"Credentials,omitempty"`
}

// AKSAuthConfig holds AKS-specific auth fields. Scope is optional and
// overrides the default AKS-managed-AAD app for private clusters with a
// custom AAD integration.
type AKSAuthConfig struct {
	CloudAuthConfig
	SubscriptionID string          `json:"SubscriptionId,omitempty"`
	ResourceGroup  string          `json:"ResourceGroup,omitempty"`
	ClusterName    string          `json:"ClusterName,omitempty"`
	Scope          string          `json:"Scope,omitempty"`
	Credentials    json.RawMessage `json:"Credentials,omitempty"`
}

// GKEAuthConfig holds GKE-specific auth fields. ProjectID, Location, and
// ClusterName uniquely identify a GKE cluster within Google Cloud and are
// required for cache keying — without them, two targets pointing at
// different projects/clusters would alias on the same cached token.
type GKEAuthConfig struct {
	CloudAuthConfig
	ProjectID   string          `json:"ProjectId,omitempty"`
	Location    string          `json:"Location,omitempty"`
	ClusterName string          `json:"ClusterName,omitempty"`
	Credentials json.RawMessage `json:"Credentials,omitempty"`
}

// OidcAuthConfig holds direct Kubernetes OIDC authentication coordinates.
type OidcAuthConfig struct {
	CloudAuthConfig
	Type     string `json:"Type"`
	Audience string `json:"Audience"`
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
	if err := guardNoUnflattenedReferences(cfg.Auth); err != nil {
		return nil, err
	}

	cfg.authType = header.Type
	cfg.authRaw = cfg.Auth
	if err := cfg.validateAuthConfig(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// guardNoUnflattenedReferences rejects an Auth block that still carries a
// formae reference envelope ({"$res":true,...} / {"$ref":...}) in place of a
// scalar.
//
// formae resolves references and replaces each one with its scalar value
// before the config reaches a plugin (internal/metastructure/resolver:
// toPluginFormat). A reference it could NOT resolve is left structurally
// intact and passed through unchanged, with no $value. So an envelope
// arriving here is never a value the plugin should unwrap — it is a
// resolution that failed upstream, and unwrapping it yields an empty
// string: an EKS token minted with an empty cluster name, an AKS token
// with no resource group, and a 401 from the API server that names none of
// this.
//
// Fail here instead, naming the field and the reference, so the cause is
// legible at config-parse time.
func guardNoUnflattenedReferences(auth json.RawMessage) error {
	var value any
	if err := json.Unmarshal(auth, &value); err != nil {
		return nil // shape errors are the caller's to report
	}
	var names []string
	collectUnflattenedReferences(value, "", &names)
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf(
		"target config Auth carries unresolved formae reference(s) in %s: "+
			"formae flattens a resolved reference to its value before the plugin sees it, "+
			"so this reference did not resolve — check that the referenced resource exists "+
			"in the stack the reference names",
		strings.Join(names, ", "),
	)
}

func collectUnflattenedReferences(value any, path string, names *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasRes := typed["$res"]; hasRes {
			*names = append(*names, path)
			return
		}
		if _, hasRef := typed["$ref"]; hasRef {
			*names = append(*names, path)
			return
		}
		for name, child := range typed {
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			collectUnflattenedReferences(child, childPath, names)
		}
	case []any:
		for i, child := range typed {
			collectUnflattenedReferences(child, fmt.Sprintf("%s[%d]", path, i), names)
		}
	}
}

// AuthType returns the auth strategy type string.
func (c *Config) AuthType() string {
	return c.authType
}

// ToK8sConfig builds a rest.Config based on the auth strategy.
func (c *Config) ToK8sConfig(ctx context.Context) (*rest.Config, error) {
	if err := c.ValidateAuthPolicy(c.deps.AllowedAuthMethods); err != nil {
		return nil, err
	}
	if c.UsesOidc() {
		return c.buildOidcConfig()
	}
	switch c.authType {
	case "Kubeconfig":
		return c.buildKubeconfigConfig()
	case "EKS":
		return c.buildCloudConfig(ctx, c.newEKSProvider)
	case "GKE":
		return c.buildCloudConfig(ctx, c.newGKEProvider)
	case "AKS":
		return c.buildCloudConfig(ctx, c.newAKSProvider)
	case "OVH":
		return c.buildCloudConfig(ctx, c.newOVHProvider)
	case "OCI":
		return c.buildCloudConfig(ctx, c.newOCIProvider)
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

// connectionDetails resolves the API server endpoint and CA bundle.
//
// Whatever the target states wins, which is what an air-gapped cluster, a
// custom endpoint, or anything formae does not model relies on. Only when a
// field is absent does a provider that implements auth.ClusterDescriber get
// asked to look it up; every other auth type requires both fields as before.
func (c *Config) connectionDetails(ctx context.Context, provider auth.AuthProvider, cloud *CloudAuthConfig) (string, []byte, error) {
	endpoint := cloud.Endpoint

	var caData []byte
	if cloud.CertificateAuthority != "" {
		decoded, err := base64.StdEncoding.DecodeString(cloud.CertificateAuthority)
		if err != nil {
			return "", nil, fmt.Errorf("failed to decode certificate authority: %w", err)
		}
		caData = decoded
	}

	if endpoint != "" && len(caData) > 0 {
		return endpoint, caData, nil
	}

	describer, ok := provider.(auth.ClusterDescriber)
	if !ok {
		return "", nil, requireAuthFields(c.authType, map[string]string{
			"Endpoint":             cloud.Endpoint,
			"CertificateAuthority": cloud.CertificateAuthority,
		})
	}

	derivedEndpoint, derivedCA, err := describer.DescribeCluster(ctx)
	if err != nil {
		return "", nil, err
	}
	if endpoint == "" {
		endpoint = derivedEndpoint
	}
	if len(caData) == 0 {
		caData = derivedCA
	}
	if endpoint == "" || len(caData) == 0 {
		return "", nil, fmt.Errorf(
			"%s auth: the cloud returned no endpoint or certificate authority for this cluster",
			c.authType)
	}
	return endpoint, caData, nil
}

func (c *Config) buildCloudConfig(ctx context.Context, providerFn func() (auth.AuthProvider, *CloudAuthConfig, error)) (*rest.Config, error) {
	provider, cloud, err := providerFn()
	if err != nil {
		return nil, err
	}

	endpoint, caData, err := c.connectionDetails(ctx, provider, cloud)
	if err != nil {
		return nil, err
	}

	cfg := &rest.Config{
		Host: endpoint,
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

// requireAuthFields rejects a cloud auth block missing a value the provider
// cannot work without. Without this an omitted ClusterName reaches STS as an
// empty x-k8s-aws-id header and the API server answers with a bare 401 that
// names nothing. The Pkl schema already marks these non-optional; this is the
// same contract enforced against configs that did not come from Pkl.
func requireAuthFields(authType string, fields map[string]string) error {
	missing := make([]string, 0, len(fields))
	for name, value := range fields {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%s auth config missing required field(s): %s",
		authType, strings.Join(missing, ", "))
}

func (c *Config) newEKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac EKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse EKS auth config: %w", err)
	}
	if err := requireAuthFields("EKS", map[string]string{
		"Endpoint":             ac.Endpoint,
		"CertificateAuthority": ac.CertificateAuthority,
		"ClusterName":          ac.ClusterName,
	}); err != nil {
		return nil, nil, err
	}
	region := ac.Region
	if region == "" {
		region = eks.RegionFromEndpoint(ac.Endpoint)
	}
	return eks.NewProvider(ac.ClusterName, region), &ac.CloudAuthConfig, nil
}

func (c *Config) newGKEProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac GKEAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse GKE auth config: %w", err)
	}
	if err := requireAuthFields("GKE", map[string]string{
		"Endpoint":             ac.Endpoint,
		"CertificateAuthority": ac.CertificateAuthority,
	}); err != nil {
		return nil, nil, err
	}
	return gke.NewProvider(ac.ProjectID, ac.Location, ac.ClusterName), &ac.CloudAuthConfig, nil
}

func (c *Config) newAKSProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac AKSAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse AKS auth config: %w", err)
	}
	// Endpoint and CertificateAuthority are deliberately absent here: AKS can
	// look both up, so requiring them would defeat the point. What it cannot
	// look anything up without is the cluster's identity, and that is enforced
	// in DescribeCluster where the error can say which field is missing.
	return aks.NewProvider(ac.SubscriptionID, ac.ResourceGroup, ac.ClusterName, ac.Scope), &ac.CloudAuthConfig, nil
}

func (c *Config) newOVHProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OVHAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OVH auth config: %w", err)
	}
	if err := requireAuthFields("OVH", map[string]string{
		"Endpoint":             ac.Endpoint,
		"CertificateAuthority": ac.CertificateAuthority,
		"ServiceName":          ac.ServiceName,
		"ClusterId":            ac.ClusterID,
	}); err != nil {
		return nil, nil, err
	}
	return ovh.NewProvider(ac.ServiceName, ac.ClusterID), &ac.CloudAuthConfig, nil
}

func (c *Config) newOCIProvider() (auth.AuthProvider, *CloudAuthConfig, error) {
	var ac OCIAuthConfig
	if err := json.Unmarshal(c.authRaw, &ac); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OCI auth config: %w", err)
	}
	if err := requireAuthFields("OCI", map[string]string{
		"Endpoint":             ac.Endpoint,
		"CertificateAuthority": ac.CertificateAuthority,
		"ClusterOcid":          ac.ClusterOCID,
	}); err != nil {
		return nil, nil, err
	}
	return oci.NewProvider(ac.ClusterOCID, ac.Region), &ac.CloudAuthConfig, nil
}
