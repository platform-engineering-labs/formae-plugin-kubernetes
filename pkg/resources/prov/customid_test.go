// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import "testing"

func TestCustomResourceID_RoundTrip(t *testing.T) {
	cases := []struct {
		apiVersion, kind, namespace, name, want string
	}{
		{"cert-manager.io/v1", "Certificate", "default", "web", "cert-manager.io/v1/Certificate/default/web"},
		{"v1", "ConfigMap", "kube-system", "cfg", "v1/ConfigMap/kube-system/cfg"},
		{"apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "widgets.example.com", "apiextensions.k8s.io/v1/CustomResourceDefinition//widgets.example.com"},
	}
	for _, c := range cases {
		got := CustomResourceID(c.apiVersion, c.kind, c.namespace, c.name)
		if got != c.want {
			t.Fatalf("CustomResourceID(%q,%q,%q,%q) = %q, want %q", c.apiVersion, c.kind, c.namespace, c.name, got, c.want)
		}
		av, k, ns, n, err := ParseCustomResourceID(got)
		if err != nil {
			t.Fatalf("ParseCustomResourceID(%q) error: %v", got, err)
		}
		if av != c.apiVersion || k != c.kind || ns != c.namespace || n != c.name {
			t.Fatalf("round-trip mismatch: got (%q,%q,%q,%q)", av, k, ns, n)
		}
	}
}

func TestParseCustomResourceID_Malformed(t *testing.T) {
	for _, bad := range []string{"", "v1", "v1/ConfigMap", "v1/ConfigMap/ns", "v1/ConfigMap/ns/"} {
		if _, _, _, _, err := ParseCustomResourceID(bad); err == nil {
			t.Fatalf("expected error for %q, got nil", bad)
		}
	}
}
