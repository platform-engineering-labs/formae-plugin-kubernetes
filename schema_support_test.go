//go:build unit

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writePkl(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSupportMap(t *testing.T) {
	dir := t.TempDir()
	// v1.33: no MutatingAdmissionPolicy; v1.36: has it.
	writePkl(t, dir, "v1.33/admissionregistration/MutatingWebhookConfiguration.pkl")
	writePkl(t, dir, "v1.33/rbac/Role.pkl")
	writePkl(t, dir, "v1.36/admissionregistration/MutatingAdmissionPolicy.pkl")
	writePkl(t, dir, "v1.33/k8s.pkl")           // top-level file, not a type
	writePkl(t, dir, "helm/v1.33/HelmChart.pkl") // non-version top dir, ignored

	m, err := buildSupportMap(dir)
	if err != nil {
		t.Fatalf("buildSupportMap: %v", err)
	}

	if !m["1.33"]["K8S::Rbac::Role"] {
		t.Error("expected K8S::Rbac::Role supported on 1.33")
	}
	if !m["1.33"]["K8S::Admissionregistration::MutatingWebhookConfiguration"] {
		t.Error("expected MutatingWebhookConfiguration on 1.33")
	}
	if m["1.33"]["K8S::Admissionregistration::MutatingAdmissionPolicy"] {
		t.Error("MutatingAdmissionPolicy must NOT be on 1.33")
	}
	if !m["1.36"]["K8S::Admissionregistration::MutatingAdmissionPolicy"] {
		t.Error("expected MutatingAdmissionPolicy on 1.36")
	}
	if _, ok := m["helm"]; ok {
		t.Error("non-version top-level dir must be ignored")
	}
}

func TestTypeUnsupportedAt(t *testing.T) {
	p := &Plugin{supportMap: map[string]map[string]bool{
		"1.33": {
			"K8S::Core::ConfigMap":                                 true,
			"K8S::Admissionregistration::MutatingWebhookConfiguration": true,
		},
		"1.36": {
			"K8S::Admissionregistration::MutatingAdmissionPolicy": true,
		},
	}}

	cases := []struct {
		version, rt string
		wantBad     bool
	}{
		{"1.33", "K8S::Admissionregistration::MutatingAdmissionPolicy", true},  // absent on 1.33
		{"1.36", "K8S::Admissionregistration::MutatingAdmissionPolicy", false}, // present on 1.36
		{"1.33", "K8S::Core::ConfigMap", false},                               // present
		{"1.99", "K8S::Core::ConfigMap", false},                               // unknown version → fail-safe
	}
	for _, tc := range cases {
		bad, reason := p.typeUnsupportedAt(tc.version, tc.rt)
		if bad != tc.wantBad {
			t.Errorf("typeUnsupportedAt(%q,%q) bad=%v want %v (reason=%q)", tc.version, tc.rt, bad, tc.wantBad, reason)
		}
		if bad && reason == "" {
			t.Errorf("expected reason when gated: %q %q", tc.version, tc.rt)
		}
	}
}
