// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/aks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth/eks"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/internal/kubernetesaudience"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
)

// AuthDependencies are immutable plugin-instance dependencies copied onto a
// parsed operation config. They never contain an operation context.
type AuthDependencies struct {
	OidcSource         plugin.OidcTokenSource
	AllowedAuthMethods []string
}

type credentialsHeader struct {
	Type string `json:"Type"`
}

// AwsOidcCredentials selects broker-backed AWS role federation.
type AwsOidcCredentials struct {
	Type    string `json:"Type"`
	RoleARN string `json:"RoleArn"`
}

// AzureOidcCredentials selects broker-backed Entra workload federation.
type AzureOidcCredentials struct {
	Type     string `json:"Type"`
	TenantID string `json:"TenantId"`
	ClientID string `json:"ClientId"`
}

// GcpOidcCredentials selects broker-backed Google workload federation.
type GcpOidcCredentials struct {
	Type                     string `json:"Type"`
	WorkloadIdentityProvider string `json:"WorkloadIdentityProvider"`
	ServiceAccountEmail      string `json:"ServiceAccountEmail,omitempty"`
}

// SetAuthDependencies copies immutable plugin dependencies onto this config.
func (c *Config) SetAuthDependencies(deps AuthDependencies) {
	c.deps = deps
	c.deps.AllowedAuthMethods = append([]string(nil), deps.AllowedAuthMethods...)
}

// UsesOidc reports whether the target explicitly selects broker-backed auth.
func (c *Config) UsesOidc() bool {
	method, err := c.AuthMethod()
	if err != nil {
		return false
	}
	return method == "Oidc" || strings.HasSuffix(method, ":Oidc")
}

// AuthMethod returns the effective operator-policy method for this target.
func (c *Config) AuthMethod() (string, error) {
	switch c.authType {
	case "EKS":
		var auth EKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return "", fmt.Errorf("EKS auth config: %w", err)
		}
		if !credentialsAbsent(auth.Credentials) {
			return "EKS:Oidc", nil
		}
		return "EKS:DefaultChain", nil
	case "AKS":
		var auth AKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return "", fmt.Errorf("AKS auth config: %w", err)
		}
		if !credentialsAbsent(auth.Credentials) {
			return "AKS:Oidc", nil
		}
		return "AKS:DefaultChain", nil
	case "GKE":
		var auth GKEAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return "", fmt.Errorf("GKE auth config: %w", err)
		}
		if !credentialsAbsent(auth.Credentials) {
			return "GKE:Oidc", nil
		}
		return "GKE:ADC", nil
	case "Oidc", "Kubeconfig", "OVH", "OCI":
		return c.authType, nil
	default:
		return "", fmt.Errorf("unsupported auth type: %s", c.authType)
	}
}

// ValidateAuthPolicy rejects a target whose effective method is absent from a
// nonempty operator allowlist. An empty list preserves legacy compatibility.
func (c *Config) ValidateAuthPolicy(allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}
	method, err := c.AuthMethod()
	if err != nil {
		return err
	}
	for _, candidate := range allowed {
		if candidate == method {
			return nil
		}
	}
	return fmt.Errorf("authentication method %s is not allowed by operator policy", method)
}

// AuthFingerprint returns a stable, opaque digest of every auth field that can
// affect authenticated behavior. CA bytes are digested before the complete
// deterministic encoding is hashed, so neither credentials nor CA material
// appear in cache keys.
func (c *Config) AuthFingerprint() (string, error) {
	identity, err := c.authIdentity()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode auth fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (c *Config) authIdentity() (map[string]string, error) {
	identity := map[string]string{"Type": c.authType}
	switch c.authType {
	case "Kubeconfig":
		var auth KubeconfigAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		identity["Context"] = auth.Context
		identity["Kubeconfig"] = auth.Kubeconfig
	case "EKS":
		var auth EKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["ClusterName"] = auth.ClusterName
		identity["Region"] = auth.Region
		identity["Profile"] = auth.Profile
		if !credentialsAbsent(auth.Credentials) {
			var credentials AwsOidcCredentials
			if err := decodeStrict(auth.Credentials, &credentials); err != nil {
				return nil, err
			}
			identity["Credentials.Type"] = credentials.Type
			identity["Credentials.RoleArn"] = credentials.RoleARN
		}
	case "AKS":
		var auth AKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["SubscriptionId"] = auth.SubscriptionID
		identity["ResourceGroup"] = auth.ResourceGroup
		identity["ClusterName"] = auth.ClusterName
		identity["Scope"] = auth.Scope
		if identity["Scope"] == "" {
			identity["Scope"] = aks.DefaultAKSScope
		}
		if !credentialsAbsent(auth.Credentials) {
			var credentials AzureOidcCredentials
			if err := decodeStrict(auth.Credentials, &credentials); err != nil {
				return nil, err
			}
			identity["Credentials.Type"] = credentials.Type
			identity["Credentials.TenantId"] = credentials.TenantID
			identity["Credentials.ClientId"] = credentials.ClientID
		}
	case "GKE":
		var auth GKEAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["ProjectId"] = auth.ProjectID
		identity["Location"] = auth.Location
		identity["ClusterName"] = auth.ClusterName
		if !credentialsAbsent(auth.Credentials) {
			var credentials GcpOidcCredentials
			if err := decodeStrict(auth.Credentials, &credentials); err != nil {
				return nil, err
			}
			identity["Credentials.Type"] = credentials.Type
			identity["Credentials.WorkloadIdentityProvider"] = credentials.WorkloadIdentityProvider
			identity["Credentials.ServiceAccountEmail"] = credentials.ServiceAccountEmail
		}
	case "Oidc":
		var auth OidcAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["Audience"] = auth.Audience
	case "OVH":
		var auth OVHAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["ServiceName"] = auth.ServiceName
		identity["ClusterId"] = auth.ClusterID
	case "OCI":
		var auth OCIAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return nil, err
		}
		addCloudIdentity(identity, auth.CloudAuthConfig)
		identity["ClusterOcid"] = auth.ClusterOCID
		identity["Region"] = auth.Region
	default:
		return nil, fmt.Errorf("unsupported auth type: %s", c.authType)
	}
	if ca := identity["CertificateAuthority"]; ca != "" {
		decoded, err := base64.StdEncoding.DecodeString(ca)
		if err != nil {
			return nil, fmt.Errorf("CertificateAuthority must be valid base64")
		}
		digest := sha256.Sum256(decoded)
		identity["CertificateAuthority"] = hex.EncodeToString(digest[:])
	}
	return identity, nil
}

func addCloudIdentity(identity map[string]string, cloud CloudAuthConfig) {
	endpoint := cloud.Endpoint
	if canonical, err := canonicalHTTPSOrigin(endpoint); err == nil {
		endpoint = canonical
	}
	identity["Endpoint"] = endpoint
	identity["CertificateAuthority"] = cloud.CertificateAuthority
}

// ClusterKey identifies the physical cluster independently of credentials or
// CA rotation. Explicit endpoints are canonicalized; kubeconfig keeps its
// established path/context selector.
func (c *Config) ClusterKey() (string, error) {
	if c.authType == "Kubeconfig" {
		var kc KubeconfigAuthConfig
		if err := json.Unmarshal(c.authRaw, &kc); err != nil {
			return "", err
		}
		return fmt.Sprintf("Kubeconfig|%s|%s", kc.Kubeconfig, kc.Context), nil
	}
	var cloud CloudAuthConfig
	if err := json.Unmarshal(c.authRaw, &cloud); err != nil {
		return "", fmt.Errorf("ClusterKey: parse %s auth: %w", c.authType, err)
	}
	if cloud.Endpoint != "" {
		endpoint := cloud.Endpoint
		if !strings.Contains(endpoint, "://") {
			endpoint = "https://" + endpoint
		}
		canonical, err := canonicalHTTPSOrigin(endpoint)
		if err != nil {
			return "", fmt.Errorf("ClusterKey: Endpoint must be an HTTPS origin")
		}
		return "Endpoint|" + canonical, nil
	}
	if c.authType == "AKS" {
		var auth AKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return "", err
		}
		return fmt.Sprintf("AKS|%s|%s|%s", auth.SubscriptionID, auth.ResourceGroup, auth.ClusterName), nil
	}
	return "", fmt.Errorf("ClusterKey: %s auth missing Endpoint", c.authType)
}

var (
	awsRoleARNPattern     = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	gcpProviderPattern    = regexp.MustCompile(`^//iam\.googleapis\.com/projects/[1-9][0-9]*/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{3,31}/providers/[a-z][a-z0-9-]{3,31}$`)
	serviceAccountPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`)
)

func (c *Config) validateAuthConfig() error {
	switch c.authType {
	case "EKS":
		var auth EKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return fmt.Errorf("EKS auth config: %w", err)
		}
		if credentialsAbsent(auth.Credentials) {
			return nil
		}
		if auth.Profile != "" {
			return fmt.Errorf("EKS auth Profile cannot be combined with Credentials")
		}
		if _, err := validateExplicitConnection(auth.CloudAuthConfig); err != nil {
			return fmt.Errorf("EKS auth %w", err)
		}
		if auth.ClusterName == "" {
			return fmt.Errorf("EKS auth ClusterName is required")
		}
		if !eks.IsCommercialRegion(auth.Region) {
			return fmt.Errorf("EKS auth Region must be an explicit commercial AWS region")
		}
		var creds AwsOidcCredentials
		if err := decodeOidcCredentials("EKS", auth.Credentials, &creds); err != nil {
			return err
		}
		if !awsRoleARNPattern.MatchString(creds.RoleARN) {
			return fmt.Errorf("EKS auth Credentials.RoleArn must be a commercial AWS IAM role ARN")
		}
	case "AKS":
		var auth AKSAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return fmt.Errorf("AKS auth config: %w", err)
		}
		if credentialsAbsent(auth.Credentials) {
			return nil
		}
		if _, err := validateExplicitConnection(auth.CloudAuthConfig); err != nil {
			return fmt.Errorf("AKS auth %w", err)
		}
		if auth.Scope != "" && auth.Scope != aks.DefaultAKSScope {
			return fmt.Errorf("AKS auth Scope must be the standard AKS scope")
		}
		var creds AzureOidcCredentials
		if err := decodeOidcCredentials("AKS", auth.Credentials, &creds); err != nil {
			return err
		}
		if !validCanonicalUUID(creds.TenantID, false) {
			return fmt.Errorf("AKS auth Credentials.TenantId must be a canonical UUID")
		}
		if !validCanonicalUUID(creds.ClientID, false) {
			return fmt.Errorf("AKS auth Credentials.ClientId must be a canonical UUID")
		}
	case "GKE":
		var auth GKEAuthConfig
		if err := json.Unmarshal(c.authRaw, &auth); err != nil {
			return fmt.Errorf("GKE auth config: %w", err)
		}
		if credentialsAbsent(auth.Credentials) {
			return nil
		}
		if _, err := validateExplicitConnection(auth.CloudAuthConfig); err != nil {
			return fmt.Errorf("GKE auth %w", err)
		}
		var creds GcpOidcCredentials
		if err := decodeOidcCredentials("GKE", auth.Credentials, &creds); err != nil {
			return err
		}
		if !gcpProviderPattern.MatchString(creds.WorkloadIdentityProvider) {
			return fmt.Errorf("GKE auth Credentials.WorkloadIdentityProvider must be a canonical workload identity provider name")
		}
		if creds.ServiceAccountEmail != "" && !serviceAccountPattern.MatchString(creds.ServiceAccountEmail) {
			return fmt.Errorf("GKE auth Credentials.ServiceAccountEmail must be a canonical service account email")
		}
	case "Oidc":
		var auth OidcAuthConfig
		if err := decodeStrict(c.authRaw, &auth); err != nil {
			return fmt.Errorf("oidc auth config: %w", err)
		}
		if _, err := validateExplicitConnection(auth.CloudAuthConfig); err != nil {
			return fmt.Errorf("oidc auth %w", err)
		}
		if !kubernetesaudience.Valid(auth.Audience) {
			return fmt.Errorf("oidc auth Audience must be urn:formae:kubernetes:<canonical UUID>")
		}
	}
	return nil
}

func credentialsAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func decodeOidcCredentials(provider string, raw json.RawMessage, dst any) error {
	var header credentialsHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return fmt.Errorf("%s auth Credentials: malformed object", provider)
	}
	if header.Type != "Oidc" {
		return fmt.Errorf("%s auth Credentials.Type must be Oidc", provider)
	}
	if err := decodeStrict(raw, dst); err != nil {
		return fmt.Errorf("%s auth Credentials: %w", provider, err)
	}
	return nil
}

func decodeStrict(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func validateExplicitConnection(connection CloudAuthConfig) (string, error) {
	endpoint, err := canonicalHTTPSOrigin(connection.Endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid Endpoint: must be an HTTPS origin")
	}
	if err := validateCertificateAuthority(connection.CertificateAuthority); err != nil {
		return "", fmt.Errorf("CertificateAuthority must be nonempty base64-encoded PEM certificates")
	}
	return endpoint, nil
}

func canonicalHTTPSOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("invalid HTTPS origin")
	}
	if parsed.Opaque != "" {
		return "", fmt.Errorf("invalid HTTPS origin")
	}
	parsed.Scheme = "https"
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || port == "443" {
		if strings.Contains(hostname, ":") {
			parsed.Host = "[" + hostname + "]"
		} else {
			parsed.Host = hostname
		}
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func validateCertificateAuthority(encoded string) error {
	if encoded == "" {
		return fmt.Errorf("empty certificate authority")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return fmt.Errorf("invalid certificate authority")
	}
	remaining := decoded
	found := false
	for len(bytes.TrimSpace(remaining)) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("invalid certificate authority")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("invalid certificate authority")
		}
		found = true
		remaining = rest
	}
	if !found {
		return fmt.Errorf("invalid certificate authority")
	}
	return nil
}

func validCanonicalUUID(value string, requireKnownVersion bool) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || strings.ToLower(value) != value {
		return false
	}
	hexValue := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(hexValue)
	if err != nil || len(decoded) != 16 {
		return false
	}
	allZero := true
	for _, b := range decoded {
		allZero = allZero && b == 0
	}
	if allZero || decoded[8]&0xc0 != 0x80 {
		return false
	}
	version := decoded[6] >> 4
	return !requireKnownVersion || (version >= 1 && version <= 5)
}
