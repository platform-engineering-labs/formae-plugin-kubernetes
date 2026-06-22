// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pluginSchemaDir returns the on-disk schema/pkl directory shipped beside the
// plugin binary. It mirrors how the formae SDK locates the plugin schema
// (filepath.Dir(os.Executable()) + schema/pkl), so the gate reads the same
// installed schema the SDK extracts from — honoring any operator edits to it.
func pluginSchemaDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate plugin binary: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "schema", "pkl"), nil
}

// buildSupportMap walks the per-version schema subtrees under schemaDir and
// returns minor-version -> set of supported resource types. A type is
// "supported" on a minor when its schema file exists at
// v<minor>/<group>/<Kind>.pkl — exactly the artifact gen-versioned-reflect
// emits (and drops for version-gated types). The resource type is
// reconstructed as "K8S::<Group>::<Kind>" with the group's first letter
// upper-cased (matching the `const type` segments in the schema).
//
// Non-version top-level entries (helm/, shared.pkl, target.pkl, k8s.pkl) are
// ignored. Reading from disk means an operator who regenerates or edits the
// installed schema is reflected without rebuilding the plugin.
func buildSupportMap(schemaDir string) (map[string]map[string]bool, error) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("read schema dir %s: %w", schemaDir, err)
	}

	out := map[string]map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		minor, ok := minorFromVersionDir(e.Name())
		if !ok {
			continue
		}
		types := map[string]bool{}
		vdir := filepath.Join(schemaDir, e.Name())
		groups, err := os.ReadDir(vdir)
		if err != nil {
			return nil, fmt.Errorf("read version dir %s: %w", vdir, err)
		}
		for _, g := range groups {
			if !g.IsDir() {
				continue // skip k8s.pkl and any top-level files
			}
			gdir := filepath.Join(vdir, g.Name())
			files, err := os.ReadDir(gdir)
			if err != nil {
				return nil, fmt.Errorf("read group dir %s: %w", gdir, err)
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".pkl") {
					continue
				}
				kind := strings.TrimSuffix(f.Name(), ".pkl")
				types[resourceTypeFor(g.Name(), kind)] = true
			}
		}
		out[minor] = types
	}
	return out, nil
}

// minorFromVersionDir maps a "v1.33"-style directory name to "1.33", reporting
// false for anything that isn't a vMAJOR.MINOR version directory.
func minorFromVersionDir(name string) (string, bool) {
	if !strings.HasPrefix(name, "v") {
		return "", false
	}
	minor := strings.TrimPrefix(name, "v")
	parts := strings.SplitN(minor, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	if _, err := strconv.Atoi(parts[0]); err != nil {
		return "", false
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return "", false
	}
	return minor, true
}

// resourceTypeFor reconstructs the canonical "K8S::<Group>::<Kind>" type from a
// schema group directory and kind file stem.
func resourceTypeFor(group, kind string) string {
	if group == "" {
		return "K8S::" + kind
	}
	g := strings.ToUpper(group[:1]) + group[1:]
	return "K8S::" + g + "::" + kind
}
