// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package k8sversion

import "testing"

func TestCheckField_PodResourceClaims(t *testing.T) {
	cases := []struct {
		name           string
		clusterVersion string
		wantErr        bool
	}{
		{"older cluster fails", "1.25", true},
		{"introduced version passes", "1.26", false},
		{"newer cluster passes", "1.32", false},
		{"current passes", "1.34", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckField("K8S::Core::Pod", "spec.resourceClaims", tc.clusterVersion)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckField(%q): got err=%v, wantErr=%v", tc.clusterVersion, err, tc.wantErr)
			}
		})
	}
}

func TestCheckField_UngatedField_AlwaysPasses(t *testing.T) {
	if err := CheckField("K8S::Core::Pod", "spec.containers", "1.20"); err != nil {
		t.Errorf("ungated field should never error, got: %v", err)
	}
}

func TestCheckField_UnknownResource_AlwaysPasses(t *testing.T) {
	if err := CheckField("K8S::Apps::Whatever", "spec.something", "1.20"); err != nil {
		t.Errorf("unregistered resource should never error, got: %v", err)
	}
}

func TestLookup(t *testing.T) {
	gate, ok := Lookup("K8S::Core::Pod", "spec.resourceClaims")
	if !ok {
		t.Fatal("expected resourceClaims to be registered")
	}
	if gate.IntroducedIn != "1.26" {
		t.Errorf("got IntroducedIn=%q, want 1.26", gate.IntroducedIn)
	}
	if gate.Reference == "" {
		t.Error("expected Reference to be set")
	}

	if _, ok := Lookup("K8S::Core::Pod", "spec.containers"); ok {
		t.Error("ungated field should not be in registry")
	}
}
