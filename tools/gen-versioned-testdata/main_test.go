// (C) 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRewriteFixtureSchemaImports pins which fixture imports get the
// `v<X.Y>/` segment. Version-independent schema dirs (helm/) ship once at the
// package root, so prefixing them would point at a path that does not exist —
// `Cannot find module @k8s/v1.33/helm/Release.pkl`.
func TestRewriteFixtureSchemaImports(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "api-group import gets the version segment",
			in:   `import "@k8s/core/Pod.pkl"`,
			want: `import "@k8s/v1.33/core/Pod.pkl"`,
		},
		{
			name: "version-independent dir stays unprefixed",
			in:   `import "@k8s/helm/Release.pkl" as helm`,
			want: `import "@k8s/helm/Release.pkl" as helm`,
		},
		{
			name: "subresources import gets the per-version rename",
			in:   `import "@k8s/k8s-subresources.pkl" as k8s`,
			want: `import "@k8s/v1.33/k8s.pkl" as k8s`,
		},
		{
			name: "idempotent on an already-rewritten import",
			in:   `import "@k8s/v1.33/core/Pod.pkl"`,
			want: `import "@k8s/v1.33/core/Pod.pkl"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.pkl")
			if err := os.WriteFile(path, []byte(c.in+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := rewriteFixtureSchemaImports(path, "1.33"); err != nil {
				t.Fatalf("rewrite: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want+"\n" {
				t.Errorf("got %q, want %q", got, c.want+"\n")
			}
		})
	}
}

// TestSharedFixtureCompatible pins the per-fixture K8s floor for shared
// fixtures that evaluate fine on every minor but cannot run on old clusters —
// podinfo 6.7.x declares kubeVersion >=1.23.0-0, so Helm refuses to install it
// on 1.22 ("chart requires kubeVersion: >=1.23.0-0 which is incompatible with
// Kubernetes v1.22.17"). The gate covers the -update/-replace variants too, so
// a lifecycle never ends up half-present in a generated tree.
func TestSharedFixtureCompatible(t *testing.T) {
	cases := []struct {
		file, target string
		want         bool
	}{
		{"helmrelease.pkl", "1.22", false},
		{"helmrelease-update.pkl", "1.22", false},
		{"helmrelease-replace.pkl", "1.21", false},
		{"helmrelease-oci.pkl", "1.22", false},
		{"helmrelease-oci-update.pkl", "1.22", false},
		{"helmrelease.pkl", "1.23", true},
		{"helmrelease-oci-update.pkl", "1.36", true},
		{"deployment.pkl", "1.21", true},
		{"deployment-update.pkl", "1.21", true},
	}
	for _, c := range cases {
		if got := sharedFixtureCompatible(c.file, c.target); got != c.want {
			t.Errorf("sharedFixtureCompatible(%q, %q) = %v, want %v", c.file, c.target, got, c.want)
		}
	}
}
