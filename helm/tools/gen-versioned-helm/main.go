// (C) 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Command gen-versioned-helm produces per-K8s-version copies of the
// formae-helm wrappers under generated/v<X.Y>/. Source of truth is
// shared/; this tool's only job is to rewrite imports and drop mappers
// that reference K8s resource modules absent in a given minor.
//
// Pipeline:
//
//  1. Read PklProject.deps.json to find the resolved k8s package path
//     (a `package://hub.../k8s@<ver>` cache dir or a local directory
//     when developing in lock-step with formae-plugin-k8s).
//
//  2. For every `v<X.Y>/` subdir in the k8s package, generate a
//     parallel `generated/v<X.Y>/` tree:
//
//     - Copy each file from shared/ recursively.
//     - Rewrite `@k8s/<group>/<Kind>.pkl` → `@k8s/v<X.Y>/<group>/<Kind>.pkl`.
//       (`@k8s/k8s.pkl` stays unchanged — that module lives at the
//       package root, not under a version subtree.)
//     - Drop mappers whose post-rewrite imports reference files that
//       the K8s minor doesn't ship; patch dispatch.pkl to remove the
//       corresponding import + branch.
//
// The codegen is idempotent: re-running with no input changes produces
// no diff.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// sharedDir is the hand-edited source of truth. The codegen reads it
	// and emits per-K8s-version copies under v<X.Y>/. Was named "shared"
	// historically — renamed to make "this is shared, the v*/ trees are
	// generated" obvious from the directory listing.
	sharedDir = "shared"
	depsJSON  = "PklProject.deps.json"
)

// k8sImportRE matches `@k8s/<group>/<Kind>.pkl` references — the only
// imports we rewrite per K8s version. `@k8s/k8s.pkl` (no group) is
// excluded by the leading slash requirement after `k8s/<group>`.
var k8sImportRE = regexp.MustCompile(`@k8s/([a-zA-Z]+)/([A-Za-z0-9]+\.pkl)`)

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen-versioned-helm: ")

	if err := chdirToProjectRoot(); err != nil {
		log.Fatalf("locate project root: %v", err)
	}

	k8sPkgDir, err := resolveK8sPackageDir()
	if err != nil {
		log.Fatalf("resolve k8s package: %v", err)
	}
	log.Printf("k8s package resolved at %s", k8sPkgDir)

	versions, err := listK8sVersions(k8sPkgDir)
	if err != nil {
		log.Fatalf("list k8s versions: %v", err)
	}
	if len(versions) == 0 {
		log.Fatalf("k8s package at %s has no v*/ subdirs", k8sPkgDir)
	}
	log.Printf("targets: %s", strings.Join(versions, " "))

	// Drop any prior v*/ trees so a removed K8s minor (or rerun against
	// an updated k8s package) leaves no orphan output. Other top-level
	// dirs (shared/, tools/, .git/, etc.) are untouched.
	if existing, err := os.ReadDir("."); err == nil {
		for _, e := range existing {
			if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
				_ = os.RemoveAll(e.Name())
			}
		}
	}

	for _, ver := range versions {
		if err := generateVersion(ver, k8sPkgDir); err != nil {
			log.Fatalf("generate %s: %v", ver, err)
		}
	}
	log.Printf("done")
}

// chdirToProjectRoot walks up from CWD until it finds a PklProject
// alongside a `shared/` directory — that's the formae-helm package root.
// Lets `go run ./tools/gen-versioned-helm` work from any subdir.
func chdirToProjectRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		_, errPkl := os.Stat(filepath.Join(dir, "PklProject"))
		_, errMain := os.Stat(filepath.Join(dir, sharedDir))
		if errPkl == nil && errMain == nil {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no PklProject + shared/ found above %s", dir)
		}
		dir = parent
	}
}

// resolveK8sPackageDir parses PklProject.deps.json and returns the
// on-disk directory holding the k8s package. Pkl resolves remote
// packages into the user's pkl cache (~/.pkl/cache/...) and local
// `import("...PklProject")` deps into their original path.
func resolveK8sPackageDir() (string, error) {
	raw, err := os.ReadFile(depsJSON)
	if err != nil {
		return "", fmt.Errorf("read %s (run `pkl project resolve .` first): %w", depsJSON, err)
	}
	var deps struct {
		ResolvedDependencies map[string]struct {
			Type string `json:"type"`
			URI  string `json:"uri"`
			Path string `json:"path"`
		} `json:"resolvedDependencies"`
	}
	if err := json.Unmarshal(raw, &deps); err != nil {
		return "", fmt.Errorf("parse %s: %w", depsJSON, err)
	}

	for key, dep := range deps.ResolvedDependencies {
		if !strings.Contains(key, "/k8s/k8s@") {
			continue
		}
		switch dep.Type {
		case "local":
			// `path` is relative to the project dir.
			abs, err := filepath.Abs(dep.Path)
			if err != nil {
				return "", err
			}
			return abs, nil
		case "remote":
			// Strip the `projectpackage://` scheme and look up the cache
			// dir. Pkl's cache layout is platform-dependent enough that
			// we'd rather error and ask the user to run `pkl project
			// resolve` than try to reproduce it.
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			cacheRoot := filepath.Join(home, ".pkl", "cache", "package-2")
			// uri form: projectpackage://hub.platform.engineering/.../k8s@0.1.1
			pkgPath := strings.TrimPrefix(dep.URI, "projectpackage://")
			candidate := filepath.Join(cacheRoot, pkgPath)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			return "", fmt.Errorf("k8s package cache not found at %s; run `pkl project resolve .` to populate it", candidate)
		default:
			return "", fmt.Errorf("k8s dep has unsupported type %q", dep.Type)
		}
	}
	return "", fmt.Errorf("no k8s dep in %s", depsJSON)
}

// listK8sVersions returns every `v<X.Y>` subdir of the k8s package, in
// ascending order.
func listK8sVersions(k8sPkgDir string) ([]string, error) {
	entries, err := os.ReadDir(k8sPkgDir)
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "v") {
			versions = append(versions, name)
		}
	}
	sort.Strings(versions)
	return versions, nil
}

// generateVersion materialises generated/<ver>/ as a per-K8s-version
// copy of shared/. Imports are rewritten to point at @k8s/<ver>/, and
// mappers whose K8s types are absent from this minor are dropped.
func generateVersion(ver, k8sPkgDir string) error {
	dstRoot := ver // v<X.Y>/ at the package root so `@formae-helm/v<X.Y>/HelmChart.pkl` resolves.

	// Phase 1: copy + rewrite, decide which mappers to drop.
	dropped := map[string]string{} // mapper basename → dropped-because reason
	err := filepath.Walk(sharedDir, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sharedDir, srcPath)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dstRoot, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}

		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		body := string(raw)
		rewritten := rewriteK8sImports(body, ver)

		// Mappers reference @k8s/<group>/<Kind>.pkl directly. If any
		// referenced file is missing in this version's K8s subtree,
		// drop the whole mapper — the user can't import what doesn't
		// exist, and our dispatch.pkl patcher prunes the entry.
		if isMapper(rel) {
			if missing := missingK8sImports(rewritten, ver, k8sPkgDir); len(missing) > 0 {
				dropped[filepath.Base(rel)] = fmt.Sprintf("missing %s in @k8s/%s/", strings.Join(missing, ", "), ver)
				return nil
			}
		}

		return os.WriteFile(dstPath, []byte(rewritten), 0o644)
	})
	if err != nil {
		return err
	}

	// Phase 2: patch dispatch.pkl to drop imports + branches for
	// dropped mappers. dispatch.pkl is itself in shared/mappers/, so
	// it was rewritten + (if it had missing imports) potentially
	// dropped above. We read it from generated/, mutate, write back.
	if len(dropped) > 0 {
		if err := patchDispatch(dstRoot, dropped, ver, k8sPkgDir); err != nil {
			return fmt.Errorf("patch dispatch: %w", err)
		}
		log.Printf("%s: dropped %d mapper(s):", ver, len(dropped))
		for name, why := range dropped {
			log.Printf("  - %s (%s)", name, why)
		}
	}

	return nil
}

// rewriteK8sImports turns `@k8s/<group>/<Kind>.pkl` into
// `@k8s/<ver>/<group>/<Kind>.pkl`. `@k8s/k8s.pkl` is unaffected.
func rewriteK8sImports(body, ver string) string {
	return k8sImportRE.ReplaceAllString(body, fmt.Sprintf("@k8s/%s/$1/$2", ver))
}

// missingK8sImports returns the rewritten import paths whose target
// files don't exist under k8sPkgDir/<ver>/. Returns nil when every
// referenced file is present.
func missingK8sImports(body, ver, k8sPkgDir string) []string {
	pattern := regexp.MustCompile(fmt.Sprintf(`@k8s/%s/([a-zA-Z]+)/([A-Za-z0-9]+\.pkl)`, regexp.QuoteMeta(ver)))
	matches := pattern.FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var missing []string
	for _, m := range matches {
		key := m[1] + "/" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		full := filepath.Join(k8sPkgDir, ver, m[1], m[2])
		if _, err := os.Stat(full); os.IsNotExist(err) {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// patchDispatch removes dispatch.pkl entries that reference dropped
// mappers. dispatch.pkl has three kinds of references per mapper:
//
//  1. `import "<base>.pkl"`
//  2. `<base>.mapXxx(...)` calls in the mapResource fn body
//  3. `kind == "Xxx"` literals in isSupported (no mapper symbol —
//     pruned by Kind name extracted from the mapper's `mapXxx` fns).
//
// The patcher reads each dropped mapper from shared/, extracts every
// `function mapXxx(` name, then walks dispatch.pkl line-by-line
// dropping anything that references the basename or one of those
// Kind names. Standalone `// <group>` comment lines that immediately
// precede deleted blocks would otherwise dangle — we drop a comment
// line when the next non-blank line was deleted.
func patchDispatch(dstRoot string, dropped map[string]string, ver, k8sPkgDir string) error {
	dispPath := filepath.Join(dstRoot, "mappers", "dispatch.pkl")
	raw, err := os.ReadFile(dispPath)
	if err != nil {
		// dispatch.pkl was itself dropped — nothing to patch.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	dropBasenames := map[string]bool{}
	dropKinds := map[string]bool{}
	for name := range dropped {
		base := strings.TrimSuffix(name, ".pkl")
		dropBasenames[base] = true
		kinds, err := mapperKinds(filepath.Join(sharedDir, "mappers", name))
		if err != nil {
			return fmt.Errorf("read kinds from %s: %w", name, err)
		}
		for _, k := range kinds {
			dropKinds[k] = true
		}
	}

	shouldDrop := func(line string) bool {
		for base := range dropBasenames {
			if strings.Contains(line, `"`+base+`.pkl"`) ||
				strings.Contains(line, base+".isSupported(") ||
				strings.Contains(line, base+".map") {
				return true
			}
		}
		for k := range dropKinds {
			if strings.Contains(line, `kind == "`+k+`"`) {
				return true
			}
		}
		return false
	}

	lines := strings.Split(string(raw), "\n")
	// First pass: mark drops.
	dropMask := make([]bool, len(lines))
	for i, line := range lines {
		if shouldDrop(line) {
			dropMask[i] = true
		}
	}
	// Second pass: drop any standalone comment line whose next
	// non-blank, non-comment line was dropped — keeps section
	// headers tied to their content.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Look ahead for next meaningful line.
		for j := i + 1; j < len(lines); j++ {
			nextTrimmed := strings.TrimSpace(lines[j])
			if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "//") {
				continue
			}
			if dropMask[j] {
				dropMask[i] = true
			}
			break
		}
	}

	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if dropMask[i] {
			continue
		}
		out = append(out, line)
	}
	return os.WriteFile(dispPath, []byte(strings.Join(out, "\n")), 0o644)
}

// mapperKinds returns every K8s Kind a mapper handles, derived from
// `function mapKindName(` definitions in the mapper file. The Kind
// name is the suffix after `map` in the function name, used by
// patchDispatch to prune `kind == "Xxx"` literals from the dropped
// mapper's dispatch entries.
func mapperKinds(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`function map([A-Z][A-Za-z0-9]+)\(`)
	matches := re.FindAllStringSubmatch(string(raw), -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out, nil
}

// isMapper reports whether rel is a per-resource-group mapper.
// dispatch.pkl + common.pkl are NOT mappers — they're plumbing.
func isMapper(rel string) bool {
	if filepath.Dir(rel) != "mappers" {
		return false
	}
	base := filepath.Base(rel)
	switch base {
	case "dispatch.pkl", "common.pkl":
		return false
	}
	return strings.HasSuffix(base, ".pkl")
}

// silence unused-fn warnings if the helper set grows.
var _ = io.EOF
