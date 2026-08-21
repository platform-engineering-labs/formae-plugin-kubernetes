// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportRewrites locks in the two regex substitutions applied by
// rewriteImports. The split-schema layout depends on both firing on the
// right shapes and (critically) NOT firing on already-rewritten content.
func TestImportRewrites(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "import master k8s-subresources → per-version k8s",
			in:   `import "../k8s-subresources.pkl" as k8s`,
			want: `import "../k8s.pkl" as k8s`,
		},
		{
			name: "module extends master k8s-subresources → per-version k8s",
			in:   `module flowschema extends "../k8s-subresources.pkl"`,
			want: `module flowschema extends "../k8s.pkl"`,
		},
		{
			name: "bare target sibling extends → climb out one level",
			in:   `open module k8sSubresources extends "target.pkl"`,
			want: `open module k8sSubresources extends "../target.pkl"`,
		},
		{
			name: "idempotent on already-rewritten target climb",
			in:   `open module k8sSubresources extends "../target.pkl"`,
			want: `open module k8sSubresources extends "../target.pkl"`,
		},
		{
			name: "idempotent on already-rewritten per-version k8s import",
			in:   `import "../k8s.pkl" as k8s`,
			want: `import "../k8s.pkl" as k8s`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := subresourcesRenameRE.ReplaceAllString(c.in, `${1}../k8s.pkl${2}`)
			got = targetSiblingClimbRE.ReplaceAllString(got, `${1}../${2}`)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestWriteVersionIndependentDirs_EmitsOnceAtRoot pins the hoisted layout:
// `helm/` lands at the generated tree root (not under v<X.Y>/) with the
// generated banner and its relative imports untouched.
func TestWriteVersionIndependentDirs_EmitsOnceAtRoot(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	body := "module release\n\nimport \"../shared.pkl\" as k8s\n"
	if err := os.MkdirAll(filepath.Join(in, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "helm", "Release.pkl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeVersionIndependentDirs(&discoverResult{}, in, out); err != nil {
		t.Fatalf("writeVersionIndependentDirs: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(out, "helm", "Release.pkl"))
	if err != nil {
		t.Fatalf("expected out/helm/Release.pkl: %v", err)
	}
	if !strings.Contains(string(got), "AUTO-GENERATED") {
		t.Error("missing generated banner")
	}
	if !strings.Contains(string(got), `import "../shared.pkl" as k8s`) {
		t.Errorf("import rewritten; want ../shared.pkl untouched, got:\n%s", got)
	}
}

// TestWriteVersionIndependentDirs_RejectsGates — a @K8sVersion gate on a
// hoisted module has no per-version copy left to filter, so emitting it would
// silently ignore the gate. Must fail loudly instead.
func TestWriteVersionIndependentDirs_RejectsGates(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(in, "helm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "helm", "Release.pkl"), []byte("module release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	disc := &discoverResult{Files: []discoveredFile{{
		Path:          "../../" + in + "/helm/Release.pkl",
		PropertyGates: []gateWithName{{PropertyName: "someField"}},
	}}}

	err := writeVersionIndependentDirs(disc, in, out)
	if err == nil {
		t.Fatal("gated module in a version-independent dir was accepted")
	}
	if !strings.Contains(err.Error(), "@K8sVersion") {
		t.Errorf("unhelpful error: %v", err)
	}
}
