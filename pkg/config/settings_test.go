// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package config

import (
	"strings"
	"testing"
	"time"
)

// TestCRDEstablishTimeout covers PLA-711: the wait for a freshly-installed
// CRD's kind to become servable is a plugin setting, and its default is high
// enough for a chart that installs CRDs alongside its controller.
func TestCRDEstablishTimeout(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"unconfigured", "", DefaultCRDEstablishTimeout},
		{"null config", `null`, DefaultCRDEstablishTimeout},
		{"key absent", `{"somethingElse":1}`, DefaultCRDEstablishTimeout},
		{"zero", `{"crdEstablishTimeoutSeconds":0}`, DefaultCRDEstablishTimeout},
		{"negative", `{"crdEstablishTimeoutSeconds":-5}`, DefaultCRDEstablishTimeout},
		{"override", `{"crdEstablishTimeoutSeconds":600}`, 10 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings.Store(nil)
			t.Cleanup(func() { settings.Store(nil) })
			if err := SetSettings([]byte(tc.raw)); err != nil {
				t.Fatalf("SetSettings(%q): %v", tc.raw, err)
			}
			if got := CRDEstablishTimeout(); got != tc.want {
				t.Errorf("CRDEstablishTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseSettingsValidatesAllowedAuthMethods(t *testing.T) {
	settings, err := ParseSettings([]byte(`{
		"allowedAuthMethods":["EKS:Oidc","AKS:Oidc","GKE:Oidc","Oidc","EKS:DefaultChain","AKS:DefaultChain","GKE:ADC","Kubeconfig","OVH","OCI"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.AllowedAuthMethods) != 10 {
		t.Fatalf("parsed %d methods, want 10", len(settings.AllowedAuthMethods))
	}

	for _, raw := range []string{
		`{"allowedAuthMethods":["Unknown"]}`,
		`{"allowedAuthMethods":[""]}`,
		`{"allowedAuthMethods":["Oidc","Oidc"]}`,
	} {
		_, err := ParseSettings([]byte(raw))
		if err == nil {
			t.Fatalf("ParseSettings(%s) accepted invalid policy", raw)
		}
		if !strings.Contains(err.Error(), "allowedAuthMethods") {
			t.Fatalf("error should name allowedAuthMethods, got %v", err)
		}
	}
}

// Malformed config is a hard error, not a silent default: the user asked for
// something and the plugin cannot honour it.
func TestSetSettingsRejectsMalformedJSON(t *testing.T) {
	if err := SetSettings([]byte(`{"crdEstablishTimeoutSeconds":`)); err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
}
