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
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("parse k8s plugin config: %w", err)
	}
	settings.Store(&s)
	return nil
}

// CRDEstablishTimeout resolves the custom-resource establish deadline,
// falling back to DefaultCRDEstablishTimeout when unset, zero, or negative.
func CRDEstablishTimeout() time.Duration {
	if s := settings.Load(); s != nil && s.CRDEstablishTimeoutSeconds > 0 {
		return time.Duration(s.CRDEstablishTimeoutSeconds) * time.Second
	}
	return DefaultCRDEstablishTimeout
}
