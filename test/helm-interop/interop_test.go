//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// formae <-> Helm interop, helm-first.
//
//	h-install(A) . f-discover . f-adopt . f-upgrade(B) . h-rollback . f-reconcile
//
// The family every earlier attempt was missing: each of those began with formae
// installing, so nothing exercised adoption of a release formae did not create.
//
// One subtest per file in testdata/interop. A chart with a `-migrate.yaml`
// sibling runs the whole chain; one without stops after adoption, because there
// is nothing to upgrade to.
//
//	go test -tags integration -run TestHelmInterop ./pkg/resources/helm/
//	go test -tags integration -run TestHelmInterop/velero ./pkg/resources/helm/
//	INTEROP_HELM=cli go test -tags integration -run TestHelmInterop ./...
//	INTEROP_KEEP=1  go test -tags integration -run TestHelmInterop ./...
//
// Needs a reachable cluster, `make install`, a running agent, and pkl on PATH.
package interop

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	// Long enough for an operator chart to settle and for discovery to sweep.
	interopDiscoveryTimeout = 7 * time.Minute
	interopPollInterval     = 5 * time.Second

	// One target for every interop run, not one per run. A target is not
	// stack-scoped, so teardown — which destroys a stack — never collects it,
	// and a per-run target quietly accumulates one row per run forever.
	// Re-applying it is a no-op, which is why Apply treats "No changes needed"
	// as success.
	interopTargetLabel = "interop-k8s"
)

func TestHelmInterop(t *testing.T) {
	requireCluster(t)

	for _, pair := range loadSpecs(t) {
		t.Run(pair.chart.name, func(t *testing.T) {
			runInteropCell(t, pair)
		})
	}
}

func runInteropCell(t *testing.T, pair specPair) {
	spec := pair.chart
	t.Logf("trait %q via %s — %s", spec.Trait, spec.TraitObject, spec.Notes)

	cell := newCell(t, pair)
	t.Logf("helm driver: %s", cell.helm.Name())
	t.Logf("namespace %s, release %s, stack %s", cell.namespace, cell.release, cell.stack)

	cell.preflight()
	t.Cleanup(cell.teardown)

	// --- 1. helm install: a release formae knows nothing about ---------------
	if err := cell.helm.Install(spec, cell.namespace, cell.release); err != nil {
		// Before formae is involved at all. A chart that cannot stand up bare is
		// a prerequisite problem — credentials, a license, a storage class this
		// cluster lacks — and reporting it as a formae regression would bury the
		// real ones. Its spec file is the place to fix it.
		t.Skipf("%s does not install bare on this cluster: %v", spec.name, err)
	}

	state := cell.state()
	assertEqual(t, "helm chart version", spec.Version, state.Version)
	assertEqual(t, "helm revision", 1, state.Revision)
	if state.Labels[formaeManagedLabel] != "" {
		t.Fatalf("release already carries %s; nothing to adopt", formaeManagedLabel)
	}
	t.Log("✓ installed, and carries no formae ownership marker")

	// --- 2. a discoverable target, and nothing else --------------------------
	cell.writeBootstrap()
	if state, message := cell.formae.Apply("reconcile", cell.path("bootstrap.pkl")); state != "Success" {
		t.Fatalf("bootstrap %s: %s", state, message)
	}
	t.Log("✓ discoverable target created")

	// --- 3. discovery --------------------------------------------------------
	cell.awaitUnmanaged()
	t.Log("✓ discovered on the $unmanaged stack")

	// --- 4. extract and adopt ------------------------------------------------
	before := cell.state().Revision
	adopted := cell.adopt()

	if stack := cell.stackOf(); stack == "" || stack == "$unmanaged" {
		t.Fatalf("release still on stack %q after adopt", stack)
	}
	// Adoption describes what is already live, so Helm is never called and the
	// revision does not move. That is what makes it safe to adopt a chart whose
	// upgrade hooks are not safe to re-run.
	assertEqual(t, "revision unchanged by adoption", before, cell.state().Revision)
	t.Log("✓ adopted as a pure bind")

	if !pair.migrates() {
		t.Logf("no %s%s.yaml — stopping after adoption", spec.name, migrateSuffix)
		cell.assertTrait("after adoption")
		return
	}

	// --- 5. formae upgrades it ----------------------------------------------
	target := pair.migrate.Version
	cell.repin(adopted, target, spec.RepoURL, pair.migrate.Values)
	if state, message := cell.formae.Apply("patch", adopted); state != "Success" {
		t.Fatalf("formae upgrade to %s ended %s: %s%s",
			target, state, orNoMessage(message), cell.helmSideReason())
	}

	state = cell.state()
	assertEqual(t, "helm chart version after f-upgrade", target, state.Version)
	assertEqual(t, "helm revision after f-upgrade", 2, state.Revision)
	// Stamped on every formae mutation, so this is the first revision in a
	// helm-first lineage to carry it — adoption did not, having called no Helm.
	if state.Labels[formaeManagedLabel] != "true" {
		t.Errorf("formae's own upgrade did not stamp %s (labels: %v)", formaeManagedLabel, state.Labels)
	}
	// formae mutates a release through Helm's upgrade action and has no rollback
	// verb, which is what decides the hook event a chart sees. Asserted here, on
	// every chart, because this is the only step where formae actually moves a
	// release: the reconcile in step 7 is refused by the drift guard in every run
	// observed so far, CI included, so nothing downstream exercises the path.
	if strings.HasPrefix(state.Description, "Rollback") {
		t.Errorf("formae moves a release with Helm's upgrade action, so revision 2 "+
			"must be recorded as one; Helm says %q", state.Description)
	}
	t.Logf("✓ upgraded by formae as %q, ownership marker now on the lineage",
		state.Description)

	// --- 6. a human rolls it back -------------------------------------------
	if err := cell.helm.Rollback(cell.namespace, cell.release, 1); err != nil {
		t.Fatalf("helm rollback: %v", err)
	}
	state = cell.state()
	assertEqual(t, "helm chart version after h-rollback", spec.Version, state.Version)
	assertEqual(t, "helm revision after h-rollback", 3, state.Revision)
	// Helm copies labels from the revision being restored, and revision 1
	// predates adoption, so the marker does not come along. Harmless — the
	// ownership guard only gates a create and formae holds a NativeID now — but
	// surprising enough that a change here should not be silent.
	t.Logf("✓ rolled back; marker on the new revision: %q", state.Labels[formaeManagedLabel])

	// --- 7. the rollback lands as drift, and reconcile converges -------------
	cell.awaitReportedVersion(spec.Version)
	t.Log("✓ formae sees the rollback as drift")

	// An explicit `apply --mode reconcile` converging on the forma is reconcile
	// working, not a policy problem. formae is the system of record: a change
	// made outside it is drift, detecting that drift is the job, and an operator
	// who then runs the converge command has asked for exactly this.
	//
	// An earlier version of this test asserted the opposite — that reconcile
	// should refuse and require --force — and reported six charts as a policy
	// violation. That was wrong twice over. It mislabelled correct behaviour,
	// and the scenario it described (a background loop silently undoing an
	// operator's rollback) is not what this step exercises: this is a
	// human-invoked apply. Whether the background reconcile loop would do the
	// same is a separate question this harness does not currently test.
	//
	// What is worth pinning is that the drift was visible first, and that the
	// convergence actually happened rather than being reported and skipped.
	outcome, message := cell.formae.ApplyExpectingRefusal("reconcile", adopted)
	after := cell.state()
	switch outcome {
	case "Success":
		assertEqual(t, "reconcile converged the release back to the forma", target, after.Version)
		assertEqual(t, "convergence produced a new revision", 4, after.Revision)
		// formae has no rollback verb: it converges by re-applying the forma,
		// which Helm performs as an upgrade. That is what decides the hook event,
		// so a chart whose pre-upgrade and pre-rollback hooks differ gets
		// pre-upgrade here — the claim in examples/helm/README.md. Asserted on
		// every chart that converges rather than only on the pre-rollback one,
		// because velero is the only chart carrying that trait and its reconcile
		// is refused, so the trait alone would never exercise this.
		if strings.HasPrefix(after.Description, "Rollback") {
			t.Errorf("formae converged by re-applying, which Helm must record as an upgrade; "+
				"revision 4 says %q", after.Description)
		}
		t.Logf("✓ reconcile converged the out-of-band rollback back to the forma, as %q",
			after.Description)
	default:
		// A refusal is legitimate too — the guard that makes an operator
		// acknowledge an out-of-band change before overwriting it. What must not
		// happen is reporting one thing and doing another, which traefik was
		// observed doing: non-Success reported, release moved anyway.
		t.Logf("reconcile did not converge (%s): %s", outcome, firstLine(orNoMessage(message)))
		assertEqual(t, "a reconcile that did not succeed must not have moved the release",
			spec.Version, after.Version)
		assertEqual(t, "a reconcile that did not succeed must not have added a revision",
			3, after.Revision)
	}

	// --- 8. the assertion that justifies this chart --------------------------
	cell.assertTrait("after the full chain")
}

// ---------------------------------------------------------------------------
// One cell
// ---------------------------------------------------------------------------

type interopCell struct {
	t      *testing.T
	spec   chartSpec
	helm   helmClient
	formae *formaeCLI

	namespace string
	release   string
	stack     string
	work      string
}

// requireCluster skips the suite rather than failing it when nothing is
// reachable, so `go test ./...` on a laptop with no cluster stays green.
func requireCluster(t *testing.T) {
	t.Helper()
	if _, err := runCmd("kubectl", "cluster-info"); err != nil {
		t.Skipf("no reachable cluster: %v", err)
	}
}

func newCell(t *testing.T, pair specPair) *interopCell {
	t.Helper()

	// A run id keeps parallel runs and a cluster shared with other work from
	// colliding. t.TempDir is cleaned up by the framework.
	//
	// The LOW hex digits of the clock: the high ones barely move, and taking
	// those handed two runs minutes apart the same id, so the second collided
	// with the first's leftovers instead of getting a fresh namespace.
	nanos := fmt.Sprintf("%x", time.Now().UnixNano())
	runID := nanos[len(nanos)-6:]
	name := sanitize(pair.chart.name, 38)

	return &interopCell{
		t:         t,
		spec:      pair.chart,
		helm:      newHelmDriver(t),
		formae:    newFormaeCLI(t),
		namespace: fmt.Sprintf("ci-%s-%s", name, runID),
		release:   sanitize(pair.chart.name, 45),
		stack:     fmt.Sprintf("ci-%s-%s", name, runID),
		work:      t.TempDir(),
	}
}

func (c *interopCell) path(name string) string { return filepath.Join(c.work, name) }

func (c *interopCell) state() *releaseState {
	c.t.Helper()
	state, err := c.helm.State(c.namespace, c.release)
	if err != nil {
		c.t.Fatalf("read release state: %v", err)
	}
	return state
}

// helmSideReason recovers why an apply failed when formae reports no message.
//
// The machine output has no error field at all — a failed ResourceUpdate carries
// its state and nothing else — so when the plugin did run, Helm's own record is
// the only account of what went wrong. When it did not run (Rejected), the
// release is untouched and says so, which is itself the answer.
func (c *interopCell) helmSideReason() string {
	state, err := c.helm.State(c.namespace, c.release)
	if err != nil || state == nil {
		return ""
	}
	detail := fmt.Sprintf(" — helm says revision %d is %s", state.Revision, state.Status)
	if out, err := runCmd("helm", "status", c.release, "-n", c.namespace); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "DESCRIPTION:") {
				detail += ": " + strings.TrimSpace(strings.TrimPrefix(line, "DESCRIPTION:"))
				break
			}
		}
	}
	return detail
}

func (c *interopCell) nativeID() string { return nativeID(c.namespace, c.release) }

func (c *interopCell) stackOf() string {
	res := c.formae.Resource(resourceTypeRelease, c.nativeID())
	if res == nil {
		return ""
	}
	stack, _ := res["Stack"].(string)
	return stack
}

// preflight refuses to start on state a previous run left behind. On a shared
// cluster a leftover namespace, especially one still Terminating, otherwise
// surfaces minutes later as an unrelated-looking failure.
func (c *interopCell) preflight() {
	c.t.Helper()
	if out, _ := runCmd("kubectl", "get", "namespace", c.namespace, "-o", "name"); strings.TrimSpace(out) != "" {
		c.t.Fatalf("namespace %s already exists", c.namespace)
	}
	c.warnForeignClusterScoped()
}

// warnForeignClusterScoped reports cluster-scoped objects named after this
// chart that belong to somebody else.
//
// Helm refuses to adopt an object it does not own, so the install fails partway
// with "X exists and cannot be imported into the current release" — which reads
// like the chart being broken. ingress-nginx skipped a whole sweep on a
// ClusterRoleBinding carrying no Helm ownership annotations at all, so it was
// not from any run of this harness and teardown could never have collected it.
//
// Reported, never deleted: an unannotated cluster-scoped object may well be a
// real installation on this cluster, and removing it is not the test's business.
func (c *interopCell) warnForeignClusterScoped() {
	kinds := "clusterrole,clusterrolebinding,customresourcedefinition"
	out, err := runCmd("kubectl", "get", kinds, "-o",
		`jsonpath={range .items[*]}{.kind}/{.metadata.name}/`+
			`{.metadata.annotations.meta\.helm\.sh/release-namespace}{"\n"}{end}`)
	if err != nil {
		return
	}
	for _, line := range strings.Fields(out) {
		parts := strings.Split(line, "/")
		if len(parts) < 2 || !strings.HasPrefix(parts[1], c.release) {
			continue
		}
		owner := ""
		if len(parts) > 2 {
			owner = parts[2]
		}
		if owner == "" {
			c.t.Logf("heads-up: %s/%s exists with no Helm ownership annotation — "+
				"if this chart claims that name the install will fail as un-importable, "+
				"and it is not this harness's to remove", parts[0], parts[1])
		} else if !strings.HasPrefix(owner, "ci-") {
			c.t.Logf("heads-up: %s/%s belongs to release namespace %q", parts[0], parts[1], owner)
		}
	}
}

func (c *interopCell) awaitUnmanaged() {
	c.t.Helper()
	deadline := time.Now().Add(interopDiscoveryTimeout)
	for time.Now().Before(deadline) {
		if c.stackOf() == "$unmanaged" {
			return
		}
		time.Sleep(interopPollInterval)
	}
	c.t.Fatalf("%s never appeared as unmanaged within %s", c.nativeID(), interopDiscoveryTimeout)
}

func (c *interopCell) awaitReportedVersion(want string) {
	c.t.Helper()
	deadline := time.Now().Add(interopDiscoveryTimeout)
	for time.Now().Before(deadline) {
		res := c.formae.Resource(resourceTypeRelease, c.nativeID())
		if props, ok := res["Properties"].(map[string]any); ok {
			if version, _ := props["version"].(string); version == want {
				return
			}
		}
		time.Sleep(interopPollInterval)
	}
	c.t.Fatalf("formae never reported version %s for %s", want, c.nativeID())
}

// adopt extracts the discovered release and binds it, unchanged.
func (c *interopCell) adopt() string {
	c.t.Helper()
	adopted := c.path("adopted.pkl")

	// Extract labels the resource after its native id and appends a dedup
	// suffix, so an exact match finds nothing — hence the trailing wildcard.
	query := fmt.Sprintf("type:%s managed:false label:%s*", resourceTypeRelease, c.nativeID())
	if err := c.formae.Extract(query, adopted); err != nil {
		c.t.Fatalf("extract: %v", err)
	}

	// Bound at the live version so the apply carries no change.
	c.repin(adopted, c.spec.Version, "", nil)

	// reconcile, and only here. The stack does not exist yet — the extracted
	// forma is what creates it — and formae refuses a patch against a stack it
	// has never seen. The usual warning against reconciling mid-adoption is
	// about a stack already holding the namespace, where reconcile would treat
	// it as absent and delete it, taking the release down. This stack holds
	// nothing but the release. Every later apply is a patch.
	if state, message := c.formae.Apply("reconcile", adopted); state != "Success" {
		c.t.Fatalf("adopt ended %s: %s", state, orNoMessage(message))
	}
	return adopted
}

var (
	dottedKeyPattern  = regexp.MustCompile(`(?m)^(\s*)([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)+)(\s*=)`)
	stackLabelPattern = regexp.MustCompile(`(?m)^(\s*)//\s*label\s*=\s*""`)
	versionPattern    = regexp.MustCompile(`(?m)^(\s*)version\s*=\s*".*"`)
	chartLinePattern  = regexp.MustCompile(`(?m)^(\s*)(chart\s*=\s*".*")`)
)

// repin makes an extracted forma appliable and points it at a version.
//
// Three fixes, none of them chart-specific:
//
// The stack. Extract leaves it commented out (`// label = ""`) for a human to
// choose; applied as-is that is an empty stack, which formae rejects outright.
//
// Dotted map keys. Extract emits map keys as bare Pkl identifiers, so a chart
// whose values contain `identity.default.schema.json` produces a key Pkl will
// not parse. Bracket-quoting is the fix.
//
// repoURL. Helm never records which repository a release came from, so extract
// cannot reconstruct it. Adoption does not need it — the stored chart is reused
// while the version matches — but a version change has to fetch, and then it is
// required.
func (c *interopCell) repin(path, version, repoURL string, values map[string]any) {
	c.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		c.t.Fatalf("read %s: %v", path, err)
	}
	text := string(raw)

	text = stackLabelPattern.ReplaceAllString(text, fmt.Sprintf(`${1}label = "%s"`, c.stack))
	text = dottedKeyPattern.ReplaceAllString(text, `${1}["${2}"]${3}`)
	text = versionPattern.ReplaceAllString(text, fmt.Sprintf(`${1}version = "%s"`, version))

	if repoURL != "" && !strings.Contains(text, "repoURL") {
		text = chartLinePattern.ReplaceAllString(text,
			fmt.Sprintf("${1}${2}\n${1}repoURL = \"%s\"", repoURL))
	}
	if len(values) > 0 {
		// Deliberately unimplemented rather than silently ignored: rewriting a
		// values block means generating Pkl for arbitrary YAML, which is the
		// complexity this design avoids by letting extract produce it. A migrate
		// spec that needs different values wants a different approach, and
		// should say so loudly rather than quietly measuring only the version.
		c.t.Fatalf("migrate values are not supported yet (%s): the forma's values come from extract", path)
	}

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		c.t.Fatalf("write %s: %v", path, err)
	}
}

func (c *interopCell) writeBootstrap() {
	c.t.Helper()

	schema, err := filepath.Abs(filepath.Join("..", "..", "schema", "pkl"))
	if err != nil {
		c.t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(c.path(name), []byte(content), 0o600); err != nil {
			c.t.Fatalf("write %s: %v", name, err)
		}
	}

	write("PklProject", fmt.Sprintf(`amends "pkl:Project"

dependencies {
  ["formae"] {
    uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@%s"
  }
  ["k8s"] = import("%s/PklProject")
}
`, formaePklPackage(), schema))

	// A target, and nothing else. Every omission here was paid for.
	//
	// No Namespace: `helm install --create-namespace` owns it, so formae does
	// not. K8S::Helm::Release declares parent = "K8S::Core::Namespace" with a
	// listParam, so discovery enumerates namespaces and lists releases inside
	// each. A namespace this forma declared became managed, and the release
	// inside it then never appeared — 420s of polling and two forced discovery
	// passes found nothing. Left unmanaged, the same release was found in 8s.
	//
	// No Stack: formae rejects a forma that would create an empty one, and the
	// only resource this run has is the release, which formae does not know yet.
	// The stack arrives with the extracted forma instead.
	write("bootstrap.pkl", fmt.Sprintf(`amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@k8s/v%s/k8s.pkl" as k8s

forma {
  new formae.Target {
    label = "%s"
    config = new k8s.Config {
      kubernetesVersion = "%s"
      auth = new k8s.KubeconfigAuth {}
    }
  }
}
`, kubeVersion(), interopTargetLabel, kubeVersion()))

	if _, err := runCmd("pkl", "project", "resolve", c.work); err != nil {
		c.t.Fatalf("pkl project resolve: %v", err)
	}
}

// assertTrait is the assertion that justifies this chart being in the set.
// Without it a cell is padding: two charts running identical assertions differ
// only in how long they take.
func (c *interopCell) assertTrait(when string) {
	c.t.Helper()

	// pre-rollback asserts on the release record rather than on an object,
	// because for this chart no object can answer the question — see
	// assertPreRollback.
	if c.spec.Trait == "pre-rollback" {
		c.assertPreRollback(when)
		return
	}

	if c.spec.TraitObject == "" {
		c.t.Logf("no traitObject for %q; cell proves the chain only", c.spec.Trait)
		return
	}
	kind, name, found := strings.Cut(c.spec.TraitObject, "/")
	if !found {
		c.t.Fatalf("traitObject %q is not Kind/name", c.spec.TraitObject)
	}
	out, _ := runCmd("kubectl", "get", strings.ToLower(kind), name, "-n", c.namespace, "-o", "name")
	present := strings.TrimSpace(out) != ""

	switch c.spec.Trait {
	case "test-hook":
		// A test hook is a CI verb, never desired state, so it must never be
		// applied by either side.
		if present {
			c.t.Errorf("test-hook object %s exists %s; it must never be applied", c.spec.TraitObject, when)
			return
		}
		c.t.Logf("✓ test-hook object %s was not applied %s", c.spec.TraitObject, when)
	default:
		c.t.Logf("✓ %s object %s %s: %s", c.spec.Trait, c.spec.TraitObject, when,
			map[bool]string{true: "present", false: "reaped or never created"}[present])
	}
}

// assertPreRollback pins which Helm verb wrote each revision, and with it which
// hook events fired.
//
// Why not look at the hook objects: velero's hooks are annotated
// `helm.sh/hook: pre-install,pre-upgrade,pre-rollback` — one set of objects for
// all three events — with `hook-delete-policy: hook-succeeded`. So the objects
// are reaped on success, and even if they survived, their existence could not say
// which of the three events created them. Presence-checking a hook object is
// unfalsifiable here, which is what made the old assertion padding.
//
// The release record can answer it. Helm stamps every revision with what produced
// it, and the verb determines the hook event: `helm rollback` fires pre-rollback,
// an upgrade fires pre-upgrade, and nothing else can. So:
//
//   - revision 3 is the out-of-band `helm rollback` — it must be recorded as a
//     rollback, which is the only revision in this scenario that fires
//     pre-rollback hooks at all.
//   - revision 4, if reconcile converged, is formae reverting values by
//     re-applying. It must be recorded as an *upgrade*. formae has no rollback
//     verb, so a revert fires pre-upgrade hooks and never pre-rollback — the
//     claim in examples/helm/README.md, which until now nothing checked.
//
// A chart whose pre-upgrade and pre-rollback hooks differ — a CRD migration that
// must run forwards only, say — is where that distinction costs something.
func (c *interopCell) assertPreRollback(when string) {
	c.t.Helper()

	history, err := c.helm.History(c.namespace, c.release)
	if err != nil {
		c.t.Errorf("pre-rollback: read history %s: %v", when, err)
		return
	}
	byRevision := map[int]releaseState{}
	for _, rev := range history {
		byRevision[rev.Revision] = rev
	}

	rollback, ok := byRevision[3]
	if !ok {
		// Only reachable for a pre-rollback chart with no -migrate sibling, which
		// never rolls back. Not a failure; the cell proved the chain.
		c.t.Logf("pre-rollback: no revision 3 %s; the chain did not reach a rollback", when)
		return
	}
	if !strings.HasPrefix(rollback.Description, "Rollback to") {
		c.t.Errorf("pre-rollback: revision 3 was the out-of-band `helm rollback`, "+
			"so Helm must record it as one and fire pre-rollback hooks; it says %q",
			rollback.Description)
		return
	}
	c.t.Logf("✓ revision 3 is %q — the rollback fired pre-rollback hooks", rollback.Description)

	converge, ok := byRevision[4]
	if !ok {
		c.t.Logf("pre-rollback: no revision 4 %s; reconcile did not converge, "+
			"so there is no formae-side revert to check", when)
		return
	}
	if strings.HasPrefix(converge.Description, "Rollback") {
		c.t.Errorf("pre-rollback: formae has no rollback verb, so its convergence "+
			"must be an upgrade; Helm recorded revision 4 as %q", converge.Description)
		return
	}
	c.t.Logf("✓ revision 4 is %q — formae reverting values is an upgrade, "+
		"so it fires pre-upgrade hooks and never pre-rollback", converge.Description)
}

// teardown dumps what a failure needs explained, then removes the cell.
//
// It deliberately does NOT leave the cluster as it found it on failure. Keeping
// a failed release alive preserves evidence, but a chart can be an admission
// controller, and a preserved one keeps enforcing. In a sweep of twenty charts,
// one failed connaisseur left its webhook live and denied the image pull of
// every chart that ran after it — fourteen cells reported as "does not install
// bare" that had nothing wrong with them. Evidence belongs in the test log,
// where it cannot affect the next cell. INTEROP_KEEP=1 keeps live state for the
// single-chart case where poking at the cluster is the point.
func (c *interopCell) teardown() {
	// Emit a verdict per cell, as the cell ends.
	//
	// go test buffers a subtest's output until the parent finishes, so a sweep
	// that is interrupted — Ctrl-C, a CI timeout, a killed background job —
	// reports nothing at all, including for the cells that already completed.
	// Twenty charts of work went that way once. This line is written as it
	// happens, so a partial log is still worth reading:
	//   grep '^INTEROP-RESULT' sweep.log
	status := "PASS"
	switch {
	case c.t.Failed():
		status = "FAIL"
	case c.t.Skipped():
		status = "SKIP"
	}
	fmt.Printf("INTEROP-RESULT %s %s trait=%s\n", status, c.spec.name, c.spec.Trait)

	if c.t.Failed() {
		c.dumpEvidence()
	}
	if os.Getenv("INTEROP_KEEP") != "" {
		c.t.Logf("INTEROP_KEEP set — leaving namespace %s, stack %s, work %s",
			c.namespace, c.stack, c.work)
		return
	}
	c.formae.Destroy(c.stack)
	_ = c.helm.Uninstall(c.namespace, c.release)
	_, _ = runCmd("kubectl", "delete", "namespace", c.namespace, "--wait=false")
	c.deleteClusterScoped()
}

// dumpEvidence writes everything a post-mortem needs into the test log.
//
// helm history and the release's values are here because they are what located
// the values round-trip bug: the forma carried an explicitly empty collection,
// the revision Helm ended up with did not, and only a revision-by-revision
// values diff showed it.
func (c *interopCell) dumpEvidence() {
	c.t.Logf("--- evidence for %s ---", c.namespace)

	if history, err := c.helm.History(c.namespace, c.release); err == nil {
		for _, rev := range history {
			c.t.Logf("  revision %d  %-10s %s", rev.Revision, rev.Status, rev.Version)
		}
	}
	for _, revision := range []string{"1", "2", "3"} {
		out, err := runCmd("helm", "get", "values", c.release,
			"-n", c.namespace, "--revision", revision)
		if err != nil {
			continue
		}
		c.t.Logf("  values @%s: %s", revision, strings.Join(strings.Fields(out), " "))
	}
	if res := c.formae.Resource(resourceTypeRelease, c.nativeID()); res != nil {
		c.t.Logf("  formae stack=%v properties=%v", res["Stack"], res["Properties"])
	}
	if out, _ := runCmd("kubectl", "get", "events", "-n", c.namespace,
		"--sort-by=.lastTimestamp", "--no-headers"); out != "" {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) > 10 {
			lines = lines[len(lines)-10:]
		}
		c.t.Logf("  recent events:\n%s", strings.Join(lines, "\n"))
	}
}

// deleteClusterScoped removes the cluster-scoped objects this cell's release
// created. Helm's uninstall handles them, but only when it runs — a chart that
// failed mid-install leaves them behind with fixed names, and the next run of
// the same chart then cannot install at all ("ClusterRole X exists and cannot
// be imported"). Matched by Helm's own ownership annotation, so nothing outside
// this cell is ever touched.
//
// CRDs are included. They were left out at first as too destructive to remove
// automatically, which cost kyverno a whole sweep: a CRD from an earlier failed
// run blocked every later install of that chart. Annotation-matching makes the
// scope identical to the ClusterRoles already removed here — only objects this
// cell's own release created.
func (c *interopCell) deleteClusterScoped() {
	kinds := "clusterrole,clusterrolebinding,validatingwebhookconfiguration," +
		"mutatingwebhookconfiguration,apiservice,customresourcedefinition"
	out, err := runCmd("kubectl", "get", kinds, "-o",
		`jsonpath={range .items[?(@.metadata.annotations.meta\.helm\.sh/release-namespace=="`+c.namespace+`")]}{.kind}/{.metadata.name}{"\n"}{end}`)
	if err != nil {
		return
	}
	for _, line := range strings.Fields(out) {
		kind, name, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		if _, err := runCmd("kubectl", "delete", strings.ToLower(kind), name, "--ignore-not-found"); err == nil {
			c.t.Logf("removed leftover %s", line)
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func assertEqual[T comparable](t *testing.T, label string, want, got T) {
	t.Helper()
	if want != got {
		t.Fatalf("%s: want %v, got %v", label, want, got)
	}
}

var unsafeName = regexp.MustCompile(`[^a-z0-9-]`)

func sanitize(name string, limit int) string {
	clean := unsafeName.ReplaceAllString(strings.ToLower(name), "-")
	if len(clean) > limit {
		clean = clean[:limit]
	}
	return strings.Trim(clean, "-")
}

func orNoMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return "(no message — the command carried no diagnostic)"
	}
	return message
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// kubeVersion is the schema subtree the generated forma imports. Override with
// INTEROP_KUBE_VERSION when the cluster is not on the default.
func kubeVersion() string {
	if version := os.Getenv("INTEROP_KUBE_VERSION"); version != "" {
		return version
	}
	return "1.33"
}

func formaePklPackage() string {
	if version := os.Getenv("INTEROP_FORMAE_PKL"); version != "" {
		return version
	}
	return "0.88.0"
}
