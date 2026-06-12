// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Command gen-versioned-testdata mirrors the schema-generation pattern for
// conformance-test fixtures: walks testdata/main/ and emits a per-K8s-version
// copy under testdata/generated/v<X.Y>/, importing the matching versioned
// schema package.
//
// Layout:
//
//	testdata/main/
//	  PklProject                # imports schema/pkl-main as @k8s
//	  shared/                   # version-agnostic fixtures (copied as-is)
//	  features/                 # gated fixtures with meta.pkl declaring minK8sVersion
//	    <feature>/
//	      meta.pkl              # { minK8sVersion = "1.26"; maxK8sVersion = "" }
//	      <fixture>.pkl
//	testdata/generated/
//	  v<X.Y>/
//	    PklProject              # imports schema/pkl/v<X.Y> as @k8s
//	    shared/                 # full copy
//	    features/               # only feature dirs whose meta is compatible
//	      <feature>/
//
// The PklProject is rewritten so each generated tree depends on the
// matching versioned schema package. PklProject.deps.json is regenerated
// by `pkl project resolve` after the rewrite (handled by the Makefile).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// PklProject path rewrites applied during testdata generation.
// Relative paths in testdata/main/PklProject resolve from `testdata/main/`;
// in the generated copy they resolve from `testdata/generated/v<X.Y>/`,
// which is one directory deeper, so each `../` becomes one more `../`.
//
// Schema dependency points at the unified `schema/pkl/`
// PklProject — a single @k8s package containing every version subtree.
// Fixture imports inside this tree are rewritten separately to address
// the matching `v<X.Y>/` subtree (see rewriteFixtureSchemaImports).
var pklProjectRewrites = func(_ string) []struct{ Old, New string } {
	return []struct{ Old, New string }{
		{
			Old: `import("../../schema/pkl-main/PklProject")`,
			New: `import("../../../schema/pkl/PklProject")`,
		},
		{
			Old: `import("../../examples/formations/PklProject")`,
			New: `import("../../../examples/formations/PklProject")`,
		},
	}
}

// fixtureSchemaImportRE matches `import "@k8s/<group>/<X>.pkl"` style
// references that target an api-group subdirectory.
var fixtureSchemaImportRE = regexp.MustCompile(`(import\s+"@k8s/)([a-z][a-z0-9_-]*\/)`)

// fixtureSubresourcesImportRE matches `import "@k8s/k8s-subresources.pkl"`
// (with or without an `as <alias>` suffix) — the master tree's
// version-agnostic subresources file used by chart-conformance fixtures
// that run against `testdata/main/` directly (not the per-version
// generated tree).
var fixtureSubresourcesImportRE = regexp.MustCompile(`(import\s+"@k8s/)k8s-subresources\.pkl(")`)

// fixtureK8sRootImportWithAliasRE matches `import "@k8s/k8s.pkl" as <alias>`
// — preserves whatever alias the fixture already declared. Applied first.
var fixtureK8sRootImportWithAliasRE = regexp.MustCompile(`(import\s+"@k8s/)k8s\.pkl(")(\s+as\s+\w+)`)

// fixtureK8sRootImportRE matches the bare `import "@k8s/k8s.pkl"` form
// (no `as <alias>`). Applied AFTER the with-alias pass to catch only the
// remaining bare imports.
var fixtureK8sRootImportRE = regexp.MustCompile(`(import\s+"@k8s/)k8s\.pkl(")`)

// rewriteFixtureSchemaImports rewrites fixture imports under a generated
// per-version testdata tree to address the matching schema version
// subtree:
//
//	import "@k8s/core/Pod.pkl"                       ->  import "@k8s/v1.30/core/Pod.pkl"
//	import "@k8s/k8s-subresources.pkl" as k8s        ->  import "@k8s/v1.30/k8s.pkl" as k8s
//	import "@k8s/k8s.pkl" as foo                     ->  import "@k8s/v1.30/k8s.pkl" as foo
//	import "@k8s/k8s.pkl"                            ->  import "@k8s/v1.30/k8s.pkl"
//
// After the schema split the package root holds only Config + Auth (in
// target.pkl); SubResource classes (PodSpec, Container, …) live in the
// master tree's k8s-subresources.pkl and the per-version v<X.Y>/k8s.pkl.
// Fixtures reach Config + Auth + SubResources through the per-version
// k8s.pkl via the extends chain (v<X.Y>/k8s.pkl → target → shared).
//
// Order matters: the subresources pattern runs before the bare k8s.pkl
// pattern so the more-specific match wins. Idempotent: re-running on
// already-rewritten content is a no-op.
func rewriteFixtureSchemaImports(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rewritten := fixtureSchemaImportRE.ReplaceAll(data, []byte(fmt.Sprintf(`${1}v%s/${2}`, version)))
	rewritten = fixtureSubresourcesImportRE.ReplaceAll(rewritten, []byte(fmt.Sprintf(`${1}v%s/k8s.pkl${2}`, version)))
	rewritten = fixtureK8sRootImportWithAliasRE.ReplaceAll(rewritten, []byte(fmt.Sprintf(`${1}v%s/k8s.pkl${2}${3}`, version)))
	rewritten = fixtureK8sRootImportRE.ReplaceAll(rewritten, []byte(fmt.Sprintf(`${1}v%s/k8s.pkl${2}`, version)))
	if bytesEqual(data, rewritten) {
		return nil
	}
	return os.WriteFile(path, rewritten, 0o644)
}

// k8sVersionEnvReadRE matches the master `vars.pkl` env-var-with-fallback
// expression that picks the K8s minor under test. Per-version generated
// trees rewrite this to a literal so fixtures are self-contained.
var k8sVersionEnvReadRE = regexp.MustCompile(`read\?\("env:FORMAE_K8S_VERSION"\)\s*\?\?\s*"[^"]*"`)

// bakeKubernetesVersion replaces the env-var lookup in a copied vars.pkl
// with a literal K8s minor matching the per-version subtree being
// generated. No-op if the file doesn't exist or doesn't contain the
// expected pattern (master may have already inlined it).
func bakeKubernetesVersion(varsPath, target string) error {
	data, err := os.ReadFile(varsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !k8sVersionEnvReadRE.Match(data) {
		return nil
	}
	rewritten := k8sVersionEnvReadRE.ReplaceAll(data, []byte(fmt.Sprintf(`"%s"`, target)))
	return os.WriteFile(varsPath, rewritten, 0o644)
}

// walkRewriteFixtures walks `root`, rewriting every .pkl file's
// `@k8s/<group>/...` schema imports to address the version subtree.
func walkRewriteFixtures(root, version string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".pkl") {
			return nil
		}
		return rewriteFixtureSchemaImports(path, version)
	})
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

type meta struct {
	MinK8sVersion string `json:"minK8sVersion,omitempty"`
	MaxK8sVersion string `json:"maxK8sVersion,omitempty"`
	Description   string `json:"description,omitempty"`
}

func cmpMinor(a, b string) int {
	am, an := splitMinor(a)
	bm, bn := splitMinor(b)
	switch {
	case am < bm:
		return -1
	case am > bm:
		return 1
	case an < bn:
		return -1
	case an > bn:
		return 1
	}
	return 0
}

func splitMinor(v string) (int, int) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	return major, minor
}

// readMeta evaluates a feature dir's meta.pkl via `pkl eval --format json`
// and unmarshals the result. The file format is:
//
//	minK8sVersion = "1.26"   // optional
//	maxK8sVersion = ""       // optional
//	description = "..."      // optional, for humans
//
// meta.pkl is read at generation time only — the gen tool does NOT copy it
// into the per-version output tree, so the harness's fixture discovery
// never sees it.
func readMeta(path string) (meta, error) {
	cmd := exec.Command("pkl", "eval", "--format", "json", path)
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	if err != nil {
		return meta{}, fmt.Errorf("pkl eval %s: %w", path, err)
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return meta{}, fmt.Errorf("parse meta %s: %w", path, err)
	}
	return m, nil
}

func (m meta) compatible(target string) bool {
	if m.MinK8sVersion != "" && cmpMinor(target, m.MinK8sVersion) < 0 {
		return false
	}
	if m.MaxK8sVersion != "" && cmpMinor(target, m.MaxK8sVersion) >= 0 {
		return false
	}
	return true
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// meta.pkl is gen-tool input, not a fixture — keep it out of the
		// generated tree so the harness doesn't try to evaluate it.
		if filepath.Base(path) == "meta.pkl" {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

func writePklProject(srcPath, dstPath, target string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	out := string(data)
	for _, r := range pklProjectRewrites(target) {
		out = strings.ReplaceAll(out, r.Old, r.New)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dstPath, []byte(out), 0o644)
}

func processTarget(masterDir, outDir, target string) error {
	dst := filepath.Join(outDir, "v"+target)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clean %s: %w", dst, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	// 1. Rewrite PklProject so it imports the versioned schema package.
	srcProject := filepath.Join(masterDir, "PklProject")
	dstProject := filepath.Join(dst, "PklProject")
	if err := writePklProject(srcProject, dstProject, target); err != nil {
		return fmt.Errorf("PklProject rewrite: %w", err)
	}

	// 2. Copy shared/ verbatim.
	sharedSrc := filepath.Join(masterDir, "shared")
	if _, err := os.Stat(sharedSrc); err == nil {
		if err := copyTree(sharedSrc, filepath.Join(dst, "shared")); err != nil {
			return fmt.Errorf("copy shared: %w", err)
		}
		// 2.1 Rewrite per-fixture schema imports to address the matching
		// version subtree under @k8s. Master fixtures use unprefixed
		// `@k8s/<group>/<X>.pkl` paths; under the unified install layout
		// each api-group lives under `@k8s/v<X.Y>/<group>/`.
		if err := walkRewriteFixtures(filepath.Join(dst, "shared"), target); err != nil {
			return fmt.Errorf("rewrite shared fixture imports: %w", err)
		}
		// 2.2 Bake the K8s minor into shared/config/vars.pkl so the
		// generated tree is self-contained — no FORMAE_K8S_VERSION env
		// var needed at conformance-run time. Master uses an env-var
		// read with a fallback; per-version tree gets a literal.
		varsFile := filepath.Join(dst, "shared", "config", "vars.pkl")
		if err := bakeKubernetesVersion(varsFile, target); err != nil {
			return fmt.Errorf("bake kubernetesVersion in vars.pkl: %w", err)
		}
	}

	// 2a. Resolve project deps so we can pkl-eval fixtures.
	if err := resolveProject(dst); err != nil {
		return fmt.Errorf("pkl project resolve %s: %w", dst, err)
	}

	// 2b. Drop fixtures whose schema imports don't exist on this target —
	// e.g. flowcontrol/v1 was added in K8s 1.29, so flowschema.pkl can't
	// evaluate against schema/pkl/v1.25 which omits FlowSchema.
	// `pkl eval` on each fixture is the most robust skip signal: if the
	// fixture imports a missing module, eval fails and the fixture is
	// removed from the per-version tree. No per-fixture metadata needed.
	sharedDst := filepath.Join(dst, "shared")
	if entries, err := os.ReadDir(sharedDst); err == nil {
		dropped := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pkl") {
				continue
			}
			fixturePath := filepath.Join(sharedDst, e.Name())
			cmd := exec.Command("pkl", "eval", "--project-dir", dst, fixturePath)
			// Fixtures read FORMAE_TEST_RUN_ID for unique resource names at
			// test time; supply a dummy at gen time so eval doesn't fail
			// on the env-var lookup. We only care here about whether
			// schema imports resolve.
			cmd.Env = append(os.Environ(), "FORMAE_TEST_RUN_ID=gen-versioned-testdata")
			out, err := cmd.CombinedOutput()
			if err == nil {
				continue
			}
			// Only drop the fixture when the error is specifically that
			// a referenced schema module is missing — that's the signal
			// the fixture targets an apiVersion not present in this K8s
			// minor's generated tree (e.g. flowcontrol/v1 on K8s 1.25).
			// Any other Pkl error (syntax, type strictness in newer pkl
			// releases, etc.) is left alone — those are pre-existing
			// fixture issues that the conformance harness handles, and
			// blanket-dropping would silently hide regressions.
			isMissingImport := bytes.Contains(out, []byte("Cannot find module")) ||
				bytes.Contains(out, []byte("Cannot resolve resource"))
			if !isMissingImport {
				if os.Getenv("GEN_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "[gen-debug] keep %s despite eval failure (not a missing-import): %v\n%s\n", fixturePath, err, out)
				}
				continue
			}
			if os.Getenv("GEN_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "[gen-debug] dropping %s (missing import for v%s):\n%s\n", fixturePath, target, out)
			}
			if rmErr := os.Remove(fixturePath); rmErr != nil {
				return fmt.Errorf("drop incompatible fixture %s: %w", fixturePath, rmErr)
			}
			dropped++
		}
		if dropped > 0 {
			fmt.Printf("  shared: %d fixtures dropped (incompatible with v%s)\n", dropped, target)
		}
	}

	// 3. Walk features/, include only those compatible with target.
	featuresSrc := filepath.Join(masterDir, "features")
	if entries, err := os.ReadDir(featuresSrc); err == nil {
		copied := 0
		skipped := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			featDir := filepath.Join(featuresSrc, e.Name())
			metaPath := filepath.Join(featDir, "meta.pkl")
			if _, err := os.Stat(metaPath); err != nil {
				return fmt.Errorf("missing meta.pkl in %s: %w", featDir, err)
			}
			m, err := readMeta(metaPath)
			if err != nil {
				return fmt.Errorf("read meta %s: %w", metaPath, err)
			}
			if !m.compatible(target) {
				skipped++
				continue
			}
			featDst := filepath.Join(dst, "features", e.Name())
			if err := copyTree(featDir, featDst); err != nil {
				return fmt.Errorf("copy feature %s: %w", e.Name(), err)
			}
			if err := walkRewriteFixtures(featDst, target); err != nil {
				return fmt.Errorf("rewrite feature fixture imports %s: %w", e.Name(), err)
			}
			copied++
		}
		fmt.Printf("  features: %d included, %d skipped\n", copied, skipped)
	}

	return nil
}

// resolveProject runs `pkl project resolve` to regenerate
// PklProject.deps.json after the rewrite.
func resolveProject(dir string) error {
	cmd := exec.Command("pkl", "project", "resolve", dir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	var (
		targets stringSliceFlag
		in      string
		outDir  string
		resolve bool
	)
	flag.Var(&targets, "target", "Target K8s minor version (repeatable)")
	flag.StringVar(&in, "in", "testdata/main", "Input directory (master testdata)")
	flag.StringVar(&outDir, "out-dir", "testdata/generated", "Output parent directory; per-target dirs named v<minor>/")
	flag.BoolVar(&resolve, "resolve", true, "Run `pkl project resolve` on each generated dir")
	flag.Parse()

	if len(targets) == 0 {
		log.Fatalf("usage: %s --target=X.Y [--target=X.Y ...] --in=testdata/main --out-dir=testdata/generated", os.Args[0])
	}

	for _, target := range targets {
		fmt.Printf("gen-versioned-testdata: target=%s\n", target)
		if err := processTarget(in, outDir, target); err != nil {
			log.Fatalf("target %s: %v", target, err)
		}
		if resolve {
			if err := resolveProject(filepath.Join(outDir, "v"+target)); err != nil {
				log.Fatalf("resolve %s: %v", target, err)
			}
		}
	}
}
