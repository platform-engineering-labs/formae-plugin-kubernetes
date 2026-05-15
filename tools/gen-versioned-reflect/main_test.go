// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// TestImportRewrites locks in the two regex substitutions applied by
// rewriteImports. The split-schema layout depends on both firing on the
// right shapes and (critically) NOT firing on already-rewritten content.
func TestImportRewrites(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "import master subresources → per-version sibling",
			in:   `import "../k8s-subresources.pkl" as k8s`,
			want: `import "../k8s.pkl" as k8s`,
		},
		{
			name: "module extends master subresources → per-version sibling",
			in:   `module flowschema extends "../k8s-subresources.pkl"`,
			want: `module flowschema extends "../k8s.pkl"`,
		},
		{
			name: "bare types sibling extends → climb out one level",
			in:   `open module k8sSubresources extends "k8s-types.pkl"`,
			want: `open module k8sSubresources extends "../k8s-types.pkl"`,
		},
		{
			name: "idempotent on already-rewritten subresources",
			in:   `import "../k8s.pkl" as k8s`,
			want: `import "../k8s.pkl" as k8s`,
		},
		{
			name: "idempotent on already-rewritten types climb",
			in:   `open module k8sSubresources extends "../k8s-types.pkl"`,
			want: `open module k8sSubresources extends "../k8s-types.pkl"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := subresourcesRenameRE.ReplaceAllString(c.in, `${1}../k8s.pkl${2}`)
			got = typesSiblingClimbRE.ReplaceAllString(got, `${1}../${2}`)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
