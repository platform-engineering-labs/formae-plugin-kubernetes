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
package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
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
	newTestRelease(t) // skips the whole test when no cluster is reachable

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
	t.Log("✓ upgraded by formae, ownership marker now on the lineage")

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

	// --- 7. reconcile absorbs, it does not correct ---------------------------
	cell.awaitReportedVersion(spec.Version)
	t.Log("✓ formae sees the rollback as drift")

	//nolint:lll // the decision is the point
	// ABSORB, by decision: the rollback is reported and left alone, and undoing
	// it takes --force. Snapping back would re-run pre-upgrade hooks to reverse
	// an operator's deliberate rollback, and for a chart with pre-rollback hooks
	// would fire the wrong event entirely. formae cannot tell a rollback from an
	// out-of-band upgrade in any case — Read surfaces neither revision direction
	// nor Info.Description — and absorb is the one policy not needing that.
	outcome, message := cell.formae.ApplyExpectingRefusal("reconcile", adopted)
	after := cell.state()
	switch {
	case outcome == "Success":
		// Errorf, not Fatalf: this is a known divergence between decided policy
		// and shipped behaviour, and letting it stop the cell would hide the
		// trait assertion below it — every full-chain chart would report the
		// same one failure and nothing else.
		t.Errorf("ROLLBACK POLICY: reconcile corrected the out-of-band rollback "+
			"(now revision %d at %s). Decided policy is absorb: report the drift, "+
			"require --force to undo it.", after.Revision, after.Version)
	default:
		t.Logf("✓ reconcile refused: %s", firstLine(orNoMessage(message)))
		assertEqual(t, "release untouched by the refused reconcile", spec.Version, after.Version)
		assertEqual(t, "no new revision from the refused reconcile", 3, after.Revision)
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

func newCell(t *testing.T, pair specPair) *interopCell {
	t.Helper()
	_, cfg := newTestRelease(t)

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
		helm:      newHelmDriver(t, cfg),
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

func (c *interopCell) nativeID() string { return prov.NativeID(c.namespace, c.release) }

func (c *interopCell) stackOf() string {
	res := c.formae.Resource(ResourceTypeRelease, c.nativeID())
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
		res := c.formae.Resource(ResourceTypeRelease, c.nativeID())
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
	query := fmt.Sprintf("type:%s managed:false label:%s*", ResourceTypeRelease, c.nativeID())
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

	schema, err := filepath.Abs(filepath.Join("..", "..", "..", "schema", "pkl"))
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
	if res := c.formae.Resource(ResourceTypeRelease, c.nativeID()); res != nil {
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
