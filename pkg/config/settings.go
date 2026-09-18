// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

// Settings are the plugin-wide knobs a user sets on the plugin's own entry in
// the agent's formae.conf.pkl (`resourcePlugins` → schema/Config.pkl
// PluginConfig), as opposed to Config above, which is per-target. The SDK hands
// them to Plugin.Configure once at startup, so the JSON keys here are the pkl
// property names verbatim.
type Settings struct {
	// CRDEstablishTimeoutSeconds bounds how long applying a
	// K8S::Custom::Resource waits for its kind to become servable when the CRD
	// is created in the same apply — by a sibling CustomResourceDefinition, or
	// by a K8S::Helm::Release that installs CRDs. Zero or unset means
	// DefaultCRDEstablishTimeout.
	CRDEstablishTimeoutSeconds int `json:"crdEstablishTimeoutSeconds"`

	// AllowedAuthMethods restricts target authentication at the operator
	// boundary. Empty preserves self-hosted legacy compatibility.
	AllowedAuthMethods []string `json:"allowedAuthMethods"`
}

var supportedAuthMethods = map[string]struct{}{
	"EKS:Oidc":         {},
	"AKS:Oidc":         {},
	"GKE:Oidc":         {},
	"Oidc":             {},
	"EKS:DefaultChain": {},
	"AKS:DefaultChain": {},
	"GKE:ADC":          {},
	"Kubeconfig":       {},
	"OVH":              {},
	"OCI":              {},
}

// DefaultCRDEstablishTimeout is used when no crdEstablishTimeoutSeconds is set.
// Generous on purpose: waiting costs nothing when the kind resolves on the
// first attempt, and giving up early fails an otherwise-correct apply.
// cert-manager's CRDs took ~66s to establish in one observed install.
const DefaultCRDEstablishTimeout = 3 * time.Minute

// ponytail: one plugin process, one settings value, written once by Configure
// before any operation is served. An atomic.Pointer keeps the race detector
// honest about the read path without a mutex or plumbing Settings through every
// provisioner constructor.
var settings atomic.Pointer[Settings]

// SetSettings records the plugin-wide settings. Called from Plugin.Configure
// with the JSON the SDK decodes out of FORMAE_PLUGIN_CONFIG.
func SetSettings(raw json.RawMessage) error {
	s, err := ParseSettings(raw)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	// Authentication policy belongs to the Plugin instance. Only the legacy
	// CRD timeout remains in this package-global compatibility path.
	settings.Store(&Settings{CRDEstablishTimeoutSeconds: s.CRDEstablishTimeoutSeconds})
	return nil
}

// ParseSettings validates plugin settings without storing instance-owned auth
// policy in package-global state.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	var s Settings
	if len(raw) == 0 || string(raw) == "null" {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{}, fmt.Errorf("parse k8s plugin config: %w", err)
	}
	seen := make(map[string]struct{}, len(s.AllowedAuthMethods))
	for _, method := range s.AllowedAuthMethods {
		if _, supported := supportedAuthMethods[method]; !supported {
			return Settings{}, fmt.Errorf("parse k8s plugin config: allowedAuthMethods contains unsupported value %q", method)
		}
		if _, duplicate := seen[method]; duplicate {
			return Settings{}, fmt.Errorf("parse k8s plugin config: allowedAuthMethods contains duplicate value %q", method)
		}
		seen[method] = struct{}{}
	}
	s.AllowedAuthMethods = append([]string(nil), s.AllowedAuthMethods...)
	return s, nil
}

// CRDEstablishTimeout resolves the custom-resource establish deadline,
// falling back to DefaultCRDEstablishTimeout when unset, zero, or negative.
func CRDEstablishTimeout() time.Duration {
	if s := settings.Load(); s != nil && s.CRDEstablishTimeoutSeconds > 0 {
		return time.Duration(s.CRDEstablishTimeoutSeconds) * time.Second
	}
	return DefaultCRDEstablishTimeout
}
