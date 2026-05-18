// (C) 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Command gen-versioned-helm produces per-K8s-version copies of the
// helm wrappers under helm/v<X.Y>/. Source of truth is helm/shared/;
// this tool's only job is to rewrite imports and drop mappers that
// reference K8s resource modules absent in a given minor.
//
// The helm wrappers live inside the K8s plugin's Pkl schema package
// (schema/pkl/generated/), so `@k8s` self-references aren't allowed —
// imports are rewritten to relative paths instead. From a generated
// helm file at helm/v<X.Y>/<file>.pkl, the K8s schema sits two levels
// up. Mapper files at helm/v<X.Y>/mappers/<file>.pkl sit three.
//
// Pipeline:
//
//  1. List `../v<X.Y>/` siblings to learn which K8s minors to emit.
//
//  2. For every minor, generate a parallel `helm/v<X.Y>/` tree:
//
//     - Copy each file from shared/ recursively.
//     - Rewrite `@k8s/<group>/<Kind>.pkl` →
//       `../../v<X.Y>/<group>/<Kind>.pkl` (or `../../../...` for
//       files nested one extra level under mappers/), and
//       `@k8s/k8s.pkl` → `<relprefix>k8s.pkl`.
//     - Drop mappers whose post-rewrite targets reference files the
//       K8s minor doesn't ship; patch dispatch.pkl to remove the
//       corresponding import + branch.
//
// The codegen is idempotent: re-running with no input changes produces
// no diff.
package main

import (
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
	// and emits per-K8s-version copies under v<X.Y>/.
	sharedDir = "shared"
)

// k8sImportRE matches `@k8s/<group>/<Kind>.pkl` references that need
// the version segment injected.
var k8sImportRE = regexp.MustCompile(`@k8s/([a-zA-Z]+)/([A-Za-z0-9]+\.pkl)`)

// k8sRootImportRE matches the bare `@k8s/k8s.pkl` import (the package
// root module — no version segment, just relative path to the root).
var k8sRootImportRE = regexp.MustCompile(`@k8s/k8s\.pkl`)

func main() {
	log.SetFlags(0)
	log.SetPrefix("gen-versioned-helm: ")

	if err := chdirToHelmRoot(); err != nil {
		log.Fatalf("locate helm root: %v", err)
	}

	k8sRoot, err := filepath.Abs("..")
	if err != nil {
		log.Fatalf("resolve k8s root: %v", err)
	}
	log.Printf("k8s schema root at %s", k8sRoot)

	versions, err := listK8sVersions(k8sRoot)
	if err != nil {
		log.Fatalf("list k8s versions: %v", err)
	}
	if len(versions) == 0 {
		log.Fatalf("no v*/ siblings to helm/ found under %s", k8sRoot)
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
		if err := generateVersion(ver, k8sRoot); err != nil {
			log.Fatalf("generate %s: %v", ver, err)
		}
	}
	log.Printf("done")
}

// chdirToHelmRoot walks up from CWD until it finds a `shared/`
// directory with a sibling `../PklProject` (the K8s schema project) —
// that pair identifies the helm subdir.
func chdirToHelmRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		_, errShared := os.Stat(filepath.Join(dir, sharedDir))
		_, errParentPkl := os.Stat(filepath.Join(dir, "..", "PklProject"))
		if errShared == nil && errParentPkl == nil {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no shared/ + ../PklProject pair found above %s", dir)
		}
		dir = parent
	}
}

// listK8sVersions returns every `v<X.Y>` directory under the K8s
// schema root (i.e. sibling to helm/), in ascending order.
func listK8sVersions(k8sRoot string) ([]string, error) {
	entries, err := os.ReadDir(k8sRoot)
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

// generateVersion materialises helm/<ver>/ as a per-K8s-version copy
// of shared/. Imports are rewritten to relative paths pointing at the
// sibling K8s v<ver>/ tree, and mappers whose K8s types are absent
// from this minor are dropped.
func generateVersion(ver, k8sRoot string) error {
	dstRoot := ver

	dropped := map[string]string{}
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

		// Depth determines how many `../` are needed to climb out of
		// helm/v<X.Y>/<...>/ back to schema/pkl/generated/. Each
		// path component beyond the v<X.Y>/ dir adds one `..`.
		// `HelmChart.pkl` (at v<X.Y>/) → 2 (helm + v<X.Y>).
		// `mappers/foo.pkl` (at v<X.Y>/mappers/) → 3.
		depth := 2 + strings.Count(rel, string(filepath.Separator))
		rewritten := rewriteK8sImports(body, ver, depth)

		// Mappers reference @k8s/<group>/<Kind>.pkl directly. If any
		// referenced file is missing in this version's K8s subtree,
		// drop the whole mapper.
		if isMapper(rel) {
			if missing := missingK8sImports(body, ver, k8sRoot); len(missing) > 0 {
				dropped[filepath.Base(rel)] = fmt.Sprintf("missing %s in v%s/", strings.Join(missing, ", "), ver[1:])
				return nil
			}
		}

		return os.WriteFile(dstPath, []byte(rewritten), 0o644)
	})
	if err != nil {
		return err
	}

	if len(dropped) > 0 {
		if err := patchDispatch(dstRoot, dropped); err != nil {
			return fmt.Errorf("patch dispatch: %w", err)
		}
		log.Printf("%s: dropped %d mapper(s):", ver, len(dropped))
		for name, why := range dropped {
			log.Printf("  - %s (%s)", name, why)
		}
	}

	return nil
}

// rewriteK8sImports turns `@k8s/...` references into relative imports
// that reach into the surrounding K8s schema tree from a file inside
// helm/. Each `../` cancels one directory; depth is 2 for v<ver>/
// files, 3 for v<ver>/mappers/ files.
//
// Two rewrites in order so the more specific pattern wins:
//   - `@k8s/k8s.pkl`               → `<prefix>v<ver>/subresources.pkl`
//   - `@k8s/<group>/<Kind>.pkl`    → `<prefix>v<ver>/<group>/<Kind>.pkl`
//
// where prefix = "../" * depth.
//
// `@k8s/k8s.pkl` resolves to the per-version `subresources.pkl` because
// the schema split moved SubResource classes (PodSpec, Container, ...)
// out of the root k8s.pkl (now `target.pkl`) and into the per-version
// `subresources.pkl`. Helm mappers use those classes as return types,
// so the package-root file (which holds only Config + Auth) would no
// longer satisfy them.
func rewriteK8sImports(body, ver string, depth int) string {
	prefix := strings.Repeat("../", depth)
	body = k8sRootImportRE.ReplaceAllString(body, prefix+ver+"/subresources.pkl")
	return k8sImportRE.ReplaceAllString(body, prefix+ver+"/$1/$2")
}

// missingK8sImports returns the unrewritten import targets whose files
// don't exist under k8sRoot/<ver>/.
func missingK8sImports(body, ver, k8sRoot string) []string {
	matches := k8sImportRE.FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var missing []string
	for _, m := range matches {
		key := m[1] + "/" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		full := filepath.Join(k8sRoot, ver, m[1], m[2])
		if _, err := os.Stat(full); os.IsNotExist(err) {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// patchDispatch removes dispatch.pkl entries that reference dropped
// mappers.
func patchDispatch(dstRoot string, dropped map[string]string) error {
	dispPath := filepath.Join(dstRoot, "mappers", "dispatch.pkl")
	raw, err := os.ReadFile(dispPath)
	if err != nil {
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
	dropMask := make([]bool, len(lines))
	for i, line := range lines {
		if shouldDrop(line) {
			dropMask[i] = true
		}
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") {
			continue
		}
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

var _ = io.EOF
