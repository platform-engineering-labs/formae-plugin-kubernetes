// (C) 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Command gen-versioned-reflect generates per-K8s-version PKL schema trees
// from schema/pkl/main/ using pkl:reflect-driven discovery.
//
// Pipeline:
//
//  1. Run `pkl eval discover.pkl` once. discover.pkl uses pkl:reflect to
//     walk every PKL module under schema/pkl/main/ and emit a JSON list of
//     every property/class/module that carries a @K8sVersion annotation.
//
//  2. For each requested target K8s minor version:
//     - Walk the master tree.
//     - For each file, if the module is gated outside the target window,
//       skip emitting the file.
//     - Otherwise, copy the file to the target output, dropping any
//       declarations whose (className, propertyName) is in the
//       "to-drop" set computed from the JSON.
//
// Usage:
//
//	gen-versioned-reflect \
//	  --target=1.32 1.33 1.34 \
//	  --in=schema/pkl/main \
//	  --out-dir=schema/pkl
//
// One output dir per target is created (e.g. schema/pkl/v1.32/).
package main

import (
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

const (
	defaultDiscoverScript = "tools/gen-versioned-reflect/discover.pkl"
	defaultProjectDir     = "schema/pkl/main"
)

type discoverResult struct {
	Files []discoveredFile `json:"files"`
}

type discoveredFile struct {
	Path          string         `json:"path"`
	ModuleName    string         `json:"moduleName"`
	ModuleGate    *gate          `json:"moduleGate"`
	ClassGates    []gateWithName `json:"classGates"`
	PropertyGates []gateWithName `json:"propertyGates"`
}

type gate struct {
	IntroducedIn string `json:"introducedIn"`
	RemovedIn    string `json:"removedIn"`
	DeprecatedIn string `json:"deprecatedIn"`
	Reference    string `json:"reference"`
}

type gateWithName struct {
	gate
	ClassName    string `json:"className"`
	PropertyName string `json:"propertyName"`
}

func (g gate) satisfied(target string) bool {
	if g.IntroducedIn != "" && cmpMinor(target, g.IntroducedIn) < 0 {
		return false
	}
	if g.RemovedIn != "" && cmpMinor(target, g.RemovedIn) >= 0 {
		return false
	}
	return true
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

// runDiscover invokes `pkl eval` on the discover script and decodes its
// JSON output. Single PKL invocation covers every file in the master tree.
func runDiscover(projectDir, scriptPath string) (*discoverResult, error) {
	cmd := exec.Command("pkl", "eval",
		"--project-dir", projectDir, scriptPath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("pkl discover failed: %s", ee.Stderr)
		}
		return nil, fmt.Errorf("pkl discover failed: %w", err)
	}
	var r discoverResult
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("decode discover output: %w", err)
	}
	return &r, nil
}

// keyFor maps an import* URI back to a path under the master tree. The
// discover script emits paths like "../../schema/pkl/main/core/Pod.pkl";
// we strip the "../../" prefix to get a path relative to the repo root.
func keyFor(uri string) string {
	const prefix = "../../"
	if strings.HasPrefix(uri, prefix) {
		return strings.TrimPrefix(uri, prefix)
	}
	return uri
}

// fileFromAbsPath returns the absolute filesystem path corresponding to
// the discover script's relative URI. The discover script lives at
// `tools/gen-versioned-reflect/discover.pkl`; relative paths in its
// import* directives resolve from there.
func absFromKey(key string) string {
	abs, err := filepath.Abs(key)
	if err != nil {
		return key
	}
	return abs
}

type fileTransform struct {
	skip            bool                       // module-level gate fails: skip the whole file
	dropProperties  map[string]map[string]bool // class → property → true
	dropClasses     map[string]bool            // className → true
}

func planTransforms(disc *discoverResult, target string) map[string]*fileTransform {
	plan := make(map[string]*fileTransform)
	for _, f := range disc.Files {
		key := keyFor(f.Path)
		t := &fileTransform{
			dropProperties: map[string]map[string]bool{},
			dropClasses:    map[string]bool{},
		}
		if f.ModuleGate != nil && !f.ModuleGate.satisfied(target) {
			t.skip = true
			plan[key] = t
			continue
		}
		for _, g := range f.PropertyGates {
			if !g.satisfied(target) {
				if t.dropProperties[g.ClassName] == nil {
					t.dropProperties[g.ClassName] = map[string]bool{}
				}
				t.dropProperties[g.ClassName][g.PropertyName] = true
			}
		}
		for _, g := range f.ClassGates {
			if !g.satisfied(target) {
				t.dropClasses[g.ClassName] = true
			}
		}
		plan[key] = t
	}
	return plan
}

// File rewriting: walk each line, track current class context, drop
// entire blocks (doc comments + annotations + declaration) for any class
// or property in the drop set.

var (
	classDeclRE  = regexp.MustCompile(`^\s*(?:open\s+|abstract\s+)?class\s+(\w+)`)
	propDeclRE   = regexp.MustCompile(`^\s*(?:hidden\s+|fixed\s+)?(\w+)\s*:`)
	docCommentRE = regexp.MustCompile(`^\s*///`)
	otherAnnRE   = regexp.MustCompile(`^\s*@\w+`)
	annK8sRE     = regexp.MustCompile(`^\s*@(?:\w+\.)?K8sVersion\s*\{[^}]*\}\s*$`)
	openBraceRE  = regexp.MustCompile(`\{\s*$`)
	blankLineRE  = regexp.MustCompile(`^\s*$`)
)

func rewriteFile(srcPath, dstPath string, t *fileTransform) error {
	if t != nil && t.skip {
		return nil
	}
	if t == nil {
		t = &fileTransform{
			dropProperties: map[string]map[string]bool{},
			dropClasses:    map[string]bool{},
		}
	}
	// Always drop the K8sVersion annotation class itself — it is
	// generator-side metadata that has no role in user-facing output.
	t.dropClasses["K8sVersion"] = true

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))

	currentClass := ""
	braceDepth := 0
	skipUntilCloseBrace := 0 // for class drops

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Track brace depth for class scoping.
		braceDepth += strings.Count(line, "{") - strings.Count(line, "}")

		if skipUntilCloseBrace > 0 {
			// We're inside a class body that's being dropped.
			if braceDepth < skipUntilCloseBrace {
				skipUntilCloseBrace = 0
				currentClass = ""
			}
			i++
			continue
		}

		if m := classDeclRE.FindStringSubmatch(line); m != nil {
			currentClass = m[1]
			if t.dropClasses[currentClass] {
				out = trimTrailingBlock(out)
				if openBraceRE.MatchString(line) {
					skipUntilCloseBrace = braceDepth
				}
				i++
				continue
			}
		}

		if m := propDeclRE.FindStringSubmatch(line); m != nil {
			propName := m[1]
			if currentClass != "" && t.dropProperties[currentClass] != nil &&
				t.dropProperties[currentClass][propName] {
				// Drop the property: trim the preceding doc/annotation block.
				out = trimTrailingBlock(out)
				i++
				continue
			}
		}

		// Strip @K8sVersion lines from kept output (they're enforcement
		// metadata only, redundant after generation).
		if annK8sRE.MatchString(line) {
			i++
			continue
		}

		out = append(out, line)
		i++

		// Class scope ends when we close back to its outer brace depth.
		if currentClass != "" && braceDepth == 0 {
			currentClass = ""
		}
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	return writePklWithBanner(dstPath, srcPath, []byte(strings.Join(out, "\n")))
}

// trimTrailingBlock walks backward from the end of `out` removing the
// preceding doc-comment / annotation lines that are bound to the now-being-
// dropped declaration. Stops at the first blank line or non-block line.
func trimTrailingBlock(out []string) []string {
	for len(out) > 0 {
		last := out[len(out)-1]
		if blankLineRE.MatchString(last) {
			out = out[:len(out)-1]
			break
		}
		if docCommentRE.MatchString(last) || otherAnnRE.MatchString(last) || annK8sRE.MatchString(last) {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return out
}

// writePklWithBanner prepends a "GENERATED — DO NOT EDIT" banner to a PKL
// file. The banner names the source path and points at the regen command,
// so anyone who lands here from a grep/search knows where to fix issues.
func writePklWithBanner(dstPath, srcPath string, body []byte) error {
	rel := srcPath
	if abs, err := filepath.Abs(srcPath); err == nil {
		if cwd, err := os.Getwd(); err == nil {
			if r, err := filepath.Rel(cwd, abs); err == nil {
				rel = r
			}
		}
	}
	banner := []byte(
		"// AUTO-GENERATED — DO NOT EDIT.\n" +
			"// Source: " + rel + "\n" +
			"// Regenerate via: make generate-versioned-schemas\n" +
			"// Edits made directly to this file will be lost on the next regen.\n" +
			"\n",
	)
	return os.WriteFile(dstPath, append(banner, body...), 0o644)
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

// subresourcesRenameRE matches `import "../k8s-subresources.pkl"` and
// `module X extends "../k8s-subresources.pkl"` in master resource modules.
// In the per-version tree the file is renamed to subresources.pkl
// (emitted as v<X.Y>/subresources.pkl by processTarget), so references
// to the master's k8s-subresources.pkl must become ../subresources.pkl
// (sibling in v<X.Y>/).
var subresourcesRenameRE = regexp.MustCompile(
	`((?:import|extends)\s+")\.\./k8s-subresources\.pkl(")`)

// targetSiblingClimbRE matches the bare `extends "target.pkl"` clause on
// the k8s-subresources.pkl module declaration. When that file is emitted
// as v<X.Y>/k8s.pkl it sits one level deeper than the generated root's
// target.pkl, so the reference must climb to ../target.pkl.
// Only matches without a leading `../` to avoid re-applying on already-
// rewritten content.
var targetSiblingClimbRE = regexp.MustCompile(
	`(extends\s+")(target\.pkl")`)

// pklProjectVersionRE matches the version assignment in a Pkl project
// `package {}` block, e.g. `version = "0.1.0"` (with whatever
// surrounding whitespace).
var pklProjectVersionRE = regexp.MustCompile(`version\s*=\s*"[^"]*"`)

// processTarget emits the per-version resource subtree for one target.
//
// Layout under `out` (after all targets processed):
//
//	out/
//	  PklProject              copied once from master (handled by the caller)
//	  k8s.pkl                 copied once from master (handled by the caller)
//	  PLUGINSCHEMAVERSIONS    written once (handled by the caller)
//	  v1.21/                  this function fills these per-target subtrees
//	    core/Pod.pkl
//	    apps/Deployment.pkl
//	    ...
//	  v1.30/...
//
// Per-version subdirs hold ONLY filtered resource modules. The master's
// PklProject and k8s.pkl are not duplicated — formae's PKL evaluator
// resolves shared types at the package root (via @k8s/k8s.pkl) regardless
// of which version subtree the importing module lives in.
func processTarget(disc *discoverResult, target, in, out string) error {
	plan := planTransforms(disc, target)
	versionDir := filepath.Join(out, "v"+target)

	if err := os.RemoveAll(versionDir); err != nil {
		return fmt.Errorf("clean %s: %w", versionDir, err)
	}

	stats := struct {
		files, written, skipped int
	}{}

	err := filepath.Walk(in, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(in, path)
		if err != nil {
			return err
		}
		base := filepath.Base(rel)

		// Top-level files: only k8s-subresources.pkl is per-version (it
		// carries the @K8sVersion-gated SubResource classes). Every other
		// top-level file is shared at the generated tree root by
		// writeRootFiles.
		// planRel is the original relative path used for plan lookups;
		// rel may be renamed below for the output destination.
		planRel := rel
		if filepath.Dir(rel) == "." {
			if rel != "k8s-subresources.pkl" {
				return nil
			}
			// Fall through: emit at v<X.Y>/subresources.pkl (renamed).
			// The per-version filename must NOT collide with the
			// package root's basename — formae extract's alias
			// derivation falls back to parent-dir-as-prefix on
			// basename collision, producing dotted identifiers like
			// `v1.34_k8s` for our dotted version dirs.
			rel = "subresources.pkl"
		}

		stats.files++

		// Other non-Pkl files (rare — currently none in master) — copy
		// into version subdir verbatim, no rewrites.
		if !strings.HasSuffix(path, ".pkl") {
			dst := filepath.Join(versionDir, rel)
			if err := copyFile(path, dst); err != nil {
				return fmt.Errorf("copy %s: %w", rel, err)
			}
			stats.written++
			return nil
		}
		_ = base // future use; avoid unused-var if non-Pkl branch removed

		// Look up the gating plan for this master file. Discover paths
		// look like "../../schema/pkl/main/<rel>"; suffix-match is
		// sufficient since the rel paths are unique within the master.
		// Use planRel (the original master path) for the lookup so that
		// the k8s-subresources.pkl → k8s.pkl rename doesn't break the match.
		var t *fileTransform
		for k, p := range plan {
			if strings.HasSuffix(k, filepath.Join(in, planRel)) {
				t = p
				break
			}
		}

		dst := filepath.Join(versionDir, rel)
		if err := rewriteFile(path, dst, t); err != nil {
			return fmt.Errorf("rewrite %s: %w", rel, err)
		}
		if t != nil && t.skip {
			stats.skipped++
			return nil
		}

		// Resource modules are now one directory level deeper than the
		// master. Rewrite `../<ns>.pkl` to `../../<ns>.pkl`. Leave
		// package-prefixed `@formae/...` imports alone — those resolve
		// through PklProject regardless of depth.
		if err := rewriteImports(dst); err != nil {
			return fmt.Errorf("rewrite imports %s: %w", rel, err)
		}
		stats.written++
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("gen-versioned-reflect: target=%s files=%d written=%d skipped=%d\n",
		target, stats.files, stats.written, stats.skipped)
	return nil
}

// rewriteImports adjusts relative imports in per-version files for the
// unified-tree layout. Two substitutions are applied in order:
//
//  1. `../k8s-subresources.pkl` → `../k8s.pkl` (the per-version rename).
//     Covers both `import` and `module X extends` forms.
//
//  2. bare `extends "target.pkl"` → `extends "../target.pkl"`.
//     Applies to the emitted v<X.Y>/subresources.pkl (formerly
//     k8s-subresources.pkl) whose module declaration references the
//     root-shared target.pkl.
//
// Idempotent: re-running on already-rewritten content is a no-op.
func rewriteImports(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	rewritten := subresourcesRenameRE.ReplaceAll(data, []byte(`${1}../subresources.pkl${2}`))
	rewritten = targetSiblingClimbRE.ReplaceAll(rewritten, []byte(`${1}../${2}`))
	if bytesEqual(data, rewritten) {
		return nil
	}
	return os.WriteFile(path, rewritten, 0o644)
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

// writeRootFiles copies the master's PklProject and the shared
// <namespace>.pkl into the unified tree root. Called once before the
// per-target loop. PklProject.deps.json is intentionally not copied —
// `pkl project resolve` regenerates it from the (unchanged) PklProject.
//
// The package version in the generated tree is bumped (default `0.1.1`
// vs master's `0.1.0`) so that consumers importing both projects (e.g.
// the testdata generator's PklProject which lives next to master in the
// repo) don't deduplicate on the URI and accidentally resolve to the
// master tree. Both projects keep `name = "k8s"`, so the `@k8s` alias
// downstream is unchanged.
func writeRootFiles(in, out, generatedVersion string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	srcProject := filepath.Join(in, "PklProject")
	data, err := os.ReadFile(srcProject)
	if err != nil {
		return fmt.Errorf("read master PklProject: %w", err)
	}
	body := pklProjectVersionRE.ReplaceAllString(string(data), `version = "`+generatedVersion+`"`)
	if err := os.WriteFile(filepath.Join(out, "PklProject"), []byte(body), 0o644); err != nil {
		return fmt.Errorf("write PklProject: %w", err)
	}
	// Shared package-root .pkl files: every *.pkl directly under the
	// master root (not under api-group subdirs) is shared. Today that's
	// `k8s.pkl`; the loop is generic so future shared files (e.g. an
	// extracted `meta.pkl`) flow through automatically.
	entries, err := os.ReadDir(in)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pkl") {
			continue
		}
		// k8s-subresources.pkl is per-version (filtered by processTarget
		// and emitted as v<X.Y>/k8s.pkl). It must not appear at the
		// generated tree root.
		if e.Name() == "k8s-subresources.pkl" {
			continue
		}
		src := filepath.Join(in, e.Name())
		dst := filepath.Join(out, e.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", e.Name(), err)
		}
	}
	return nil
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var (
		targets         stringSliceFlag
		in              string
		outDir          string
		discPkl         string
		projDir         string
		generatedVersion string
	)
	flag.Var(&targets, "target", "Target K8s minor version (repeatable, e.g. -target=1.32 -target=1.33)")
	flag.StringVar(&in, "in", defaultProjectDir, "Input directory (master schema)")
	flag.StringVar(&outDir, "out-dir", "schema/pkl/generated", "Output parent directory; per-target dirs named v<minor>/")
	flag.StringVar(&discPkl, "discover", defaultDiscoverScript, "Path to discover.pkl")
	flag.StringVar(&projDir, "project-dir", defaultProjectDir, "PKL --project-dir for discover invocation")
	flag.StringVar(&generatedVersion, "generated-pkl-package-version", "0.1.1",
		"Package version stamped on the generated tree's PklProject. Must differ from the master's version so Pkl's resolver doesn't dedupe the two URIs.")
	flag.Parse()

	if len(targets) == 0 {
		log.Fatalf("usage: %s --target=X.Y [--target=X.Y ...] --in=schema/pkl/main --out-dir=schema/pkl", os.Args[0])
	}

	disc, err := runDiscover(projDir, discPkl)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	totalGates := 0
	for _, f := range disc.Files {
		totalGates += len(f.PropertyGates) + len(f.ClassGates)
		if f.ModuleGate != nil {
			totalGates++
		}
	}
	fmt.Printf("gen-versioned-reflect: discovered %d files, %d gates\n", len(disc.Files), totalGates)

	// Wipe every top-level entry under outDir EXCEPT `helm/` — the
	// helm wrappers ship inside this same package (schema/pkl/generated/
	// helm/) and their source-of-truth + per-version generated trees
	// are regenerated by gen-versioned-helm in a separate step.
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, e := range entries {
			if e.Name() == "helm" {
				continue
			}
			if err := os.RemoveAll(filepath.Join(outDir, e.Name())); err != nil {
				log.Fatalf("clean %s/%s: %v", outDir, e.Name(), err)
			}
		}
	} else if !os.IsNotExist(err) {
		log.Fatalf("read %s: %v", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create %s: %v", outDir, err)
	}
	if err := writeRootFiles(in, outDir, generatedVersion); err != nil {
		log.Fatalf("root files: %v", err)
	}
	for _, target := range targets {
		_ = absFromKey // keep helper available if needed later
		if err := processTarget(disc, target, in, outDir); err != nil {
			log.Fatalf("target %s: %v", target, err)
		}
	}
	fmt.Printf("gen-versioned-reflect: wrote unified tree at %s (%d versions)\n",
		outDir, len(targets))
}
