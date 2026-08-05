//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm


import (
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

const manifest = `---
# Source: app/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-web
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata:
  name: app-web
  namespace: explicit-ns
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
`

func TestIndexManifest_DefaultsNamespaceAndIndexesEveryDoc(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	if got := inv.Len(); got != 3 {
		t.Fatalf("indexed %d objects, want 3", got)
	}

	// No explicit namespace -> the release namespace applies.
	if owner, ok := inv.OwnedBy("Deployment", "release-ns", "app-web"); !ok || owner != "release-ns/app" {
		t.Errorf("Deployment not owned by release: owner=%q ok=%v", owner, ok)
	}

	// An explicit namespace in the manifest wins over the release namespace.
	if _, ok := inv.OwnedBy("Service", "explicit-ns", "app-web"); !ok {
		t.Error("Service with explicit namespace not indexed under it")
	}
	if _, ok := inv.OwnedBy("Service", "release-ns", "app-web"); ok {
		t.Error("Service indexed under the release namespace, overriding its explicit one")
	}

	// Cluster-scoped kinds reach the filter with no namespace, so both forms
	// must resolve.
	if _, ok := inv.OwnedBy("CustomResourceDefinition", "", "widgets.example.com"); !ok {
		t.Error("cluster-scoped object not matchable without a namespace")
	}
}

func TestSplitYAMLDocs_IgnoresSeparatorInsideValues(t *testing.T) {
	// A "---" that is not a document separator must not split, or the object
	// after it is lost from the inventory and resurfaces as unmanaged.
	src := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\ndata:\n  key: \"a --- b\"\n"
	if got := len(splitYAMLDocs(src)); got != 1 {
		t.Fatalf("split into %d docs, want 1", got)
	}
}

func TestSplitYAMLDocs_SkipsEmptyDocuments(t *testing.T) {
	// Helm emits trailing separators and comment-only documents for templates
	// that render to nothing under the supplied values.
	src := "---\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n---\n"
	if got := len(splitYAMLDocs(src)); got != 1 {
		t.Fatalf("split into %d docs, want 1", got)
	}
}

func TestFilterHelmOwned_DropsOnlyChartObjects(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	ids := []string{
		"release-ns/app-web",  // rendered by the chart -> hidden
		"release-ns/user-own", // hand-authored -> kept
	}
	got := FilterHelmOwned(inv, "K8S::Apps::Deployment", ids)

	if len(got) != 1 || got[0] != "release-ns/user-own" {
		t.Fatalf("filtered to %v, want [release-ns/user-own]", got)
	}
}

func TestFilterHelmOwned_KindMustMatch(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	// Same namespace/name as the chart's Deployment, different kind. Filtering
	// on namespace/name alone would wrongly hide this.
	got := FilterHelmOwned(inv, "K8S::Core::ConfigMap", []string{"release-ns/app-web"})
	if len(got) != 1 {
		t.Fatalf("ConfigMap named like the chart's Deployment was hidden: %v", got)
	}
}

func TestFilterHelmOwned_PassesThroughUnknownResourceType(t *testing.T) {
	inv := newInventory()
	indexManifest(inv, manifest, "release-ns", "release-ns/app")

	// A type that is not NAMESPACE::Service::Resource yields no kind; dropping
	// everything would erase the whole list from discovery.
	got := FilterHelmOwned(inv, "NotAResourceType", []string{"release-ns/app-web"})
	if len(got) != 1 {
		t.Fatalf("unknown resource type filtered its ids: %v", got)
	}
}

func TestKindFromResourceType(t *testing.T) {
	for in, want := range map[string]string{
		"K8S::Apps::Deployment": "Deployment",
		"K8S::Core::ConfigMap":  "ConfigMap",
		"K8S::Helm::Release":    "Release",
		"too::few":              "",
		"":                      "",
	} {
		if got := KindFromResourceType(in); got != want {
			t.Errorf("KindFromResourceType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	// The RequestID is the only handle Status gets on an install: Create
	// withholds the NativeID until the release is deployed, so namespace and
	// name have to survive this round trip or Status cannot find the release.
	id := requestID("monitoring", "prometheus", 7, opUpgrade)
	ns, name, rev, op, err := parseRequestID(id)
	if err != nil {
		t.Fatalf("parseRequestID(%q): %v", id, err)
	}
	if ns != "monitoring" || name != "prometheus" || rev != 7 || op != opUpgrade {
		t.Fatalf("got %s/%s@%d:%s, want monitoring/prometheus@7:upgrade", ns, name, rev, op)
	}
}

func TestParseRequestID_RejectsMalformed(t *testing.T) {
	// A request id Status cannot parse must error rather than silently default
	// to install or to an empty namespace, which would send the lookup at the
	// wrong release.
	for _, id := range []string{
		"",
		"ns/name",
		"ns/name@x:install",
		"ns/name:install",
		"name@1:install",  // no namespace
		"/name@1:install", // empty namespace
		"ns/@1:install",   // empty name
		"a/b/c@1:install", // too many segments
	} {
		if _, _, _, _, err := parseRequestID(id); err == nil {
			t.Errorf("parseRequestID(%q) accepted a malformed id", id)
		}
	}
}

func TestNativeIDUnless(t *testing.T) {
	if got := nativeIDUnless(true, "ns", "name"); got != "" {
		t.Errorf("withheld native id = %q, want empty", got)
	}
	if got := nativeIDUnless(false, "ns", "name"); got != "ns/name" {
		t.Errorf("native id = %q, want ns/name", got)
	}
}

func relAt(status release.Status, revision int, age time.Duration) *release.Release {
	r := &release.Release{
		Version: revision,
		Info: &release.Info{
			Status:       status,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-age)},
		},
	}
	// Owned by this plugin unless a test says otherwise.
	r.Labels = map[string]string{formaeManagedLabel: "true"}
	return r
}

func foreignRelAt(status release.Status, revision int) *release.Release {
	r := relAt(status, revision, time.Minute)
	r.Labels = map[string]string{"someone": "else"}
	return r
}

func TestPlanSubmit_NoReleaseInstallsRevisionOne(t *testing.T) {
	action, target := planSubmit(nil, true)
	if action != actionInstall || target != 1 {
		t.Fatalf("got action=%d target=%d, want install/1", action, target)
	}
}

func TestPlanSubmit_SettledReleaseUpgradesToNextRevision(t *testing.T) {
	// Helm accepts an upgrade from deployed, failed and superseded alike
	// (upgrade.go:226-234), so all three must plan an upgrade rather than stall.
	for _, status := range []release.Status{
		release.StatusDeployed,
		release.StatusFailed,
		release.StatusSuperseded,
	} {
		action, target := planSubmit(relAt(status, 4, time.Minute), false)
		if action != actionUpgrade || target != 5 {
			t.Errorf("status %s: got action=%d target=%d, want upgrade/5", status, action, target)
		}
	}
}

// The regression this whole refactor exists for.
//
// A live pending release must plan a RETRY. The bug was reporting InProgress
// against the in-flight revision: once that revision settled, Status saw the
// release deployed at the expected revision and reported Success for work that
// never ran, so the requested change was dropped while formae recorded success.
func TestPlanSubmit_LivePendingReleaseRetriesRatherThanClaimingProgress(t *testing.T) {
	for _, status := range []release.Status{
		release.StatusPendingInstall,
		release.StatusPendingUpgrade,
		release.StatusPendingRollback,
	} {
		action, target := planSubmit(relAt(status, 2, 5*time.Second), false)
		if action != actionRetry {
			t.Errorf("status %s: got action=%d, want actionRetry", status, action)
		}
		// Must NOT be read as "revision 2 is what we asked for".
		if action == actionUpgrade && target == 2 {
			t.Errorf("status %s: planned an upgrade to the in-flight revision", status)
		}
	}
}

func TestPlanSubmit_AbandonedPendingReleaseIsBlockedNotRetried(t *testing.T) {
	// Nothing is coming back to finish it, so retrying just burns the attempt
	// budget before failing with a less useful message.
	action, _ := planSubmit(relAt(release.StatusPendingInstall, 1, 3*defaultTimeoutSeconds*time.Second), true)
	if action != actionBlocked {
		t.Fatalf("got action=%d, want actionBlocked", action)
	}
}

func TestStalled_OnlyAfterTwiceTheOperationTimeout(t *testing.T) {
	pendingSince := func(d time.Duration) *release.Release {
		return &release.Release{Info: &release.Info{
			Status:       release.StatusPendingInstall,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-d)},
		}}
	}

	// A slow pre-install hook is indistinguishable from a dead process, so the
	// window has to be wide enough that healing never races a live install.
	if stalled(pendingSince(time.Minute)) {
		t.Error("a one-minute-old pending release was called stalled")
	}
	if !stalled(pendingSince(3 * defaultTimeoutSeconds * time.Second)) {
		t.Error("a long-abandoned pending release was not called stalled")
	}
	if stalled(&release.Release{Info: &release.Info{Status: release.StatusPendingInstall}}) {
		t.Error("a release with no timestamp was called stalled")
	}
}

// The adoption guard. Create means formae believes the release does not exist,
// so a settled release it did not install is someone else's — taking it over
// would rewrite that release's history and make `helm rollback` roll back to
// revisions this forma never described.
func TestPlanSubmit_ForeignReleaseIsRefusedOnCreate(t *testing.T) {
	action, _ := planSubmit(foreignRelAt(release.StatusDeployed, 7), true)
	if action != actionForeign {
		t.Fatalf("got action=%d, want actionForeign", action)
	}
}

// Once adopted, formae holds a NativeID, so the operation arrives as Update and
// the guard must stand aside — otherwise adoption could never complete.
func TestPlanSubmit_ForeignReleaseIsUpgradableOnUpdate(t *testing.T) {
	action, target := planSubmit(foreignRelAt(release.StatusDeployed, 7), false)
	if action != actionUpgrade || target != 8 {
		t.Fatalf("got action=%d target=%d, want upgrade/8", action, target)
	}
}

// Create withholds the NativeID until the release is deployed, so a failed first
// install is retried as a Create. Our own marker is what lets that retry through
// instead of being mistaken for a foreign release.
func TestPlanSubmit_OwnFailedInstallIsRetakenOnCreate(t *testing.T) {
	action, target := planSubmit(relAt(release.StatusFailed, 1, time.Minute), true)
	if action != actionUpgrade || target != 2 {
		t.Fatalf("got action=%d target=%d, want upgrade/2", action, target)
	}
}

func TestFormaeOwns(t *testing.T) {
	if formaeOwns(nil) {
		t.Error("nil release reported as owned")
	}
	if formaeOwns(&release.Release{}) {
		t.Error("unlabelled release reported as owned")
	}
	if !formaeOwns(relAt(release.StatusDeployed, 1, time.Minute)) {
		t.Error("marked release not reported as owned")
	}
}

func TestReleaseLabels_PreservesUserLabelsAndAddsMarker(t *testing.T) {
	got := releaseLabels(&releaseProperties{
		Metadata: releaseMetadata{Labels: map[string]string{"team": "identity"}},
	})
	if got["team"] != "identity" {
		t.Errorf("user label dropped: %v", got)
	}
	if got[formaeManagedLabel] != "true" {
		t.Errorf("ownership marker missing: %v", got)
	}
}

// Helm's own bookkeeping labels must never reach a forma or come back to Helm.
// The secrets driver filters them on Get but not on the list/last paths
// (driver/secrets.go:103,141), so an extracted forma picked them up and Helm then
// rejected the next upgrade outright.
func TestWithoutSystemLabels(t *testing.T) {
	got := withoutSystemLabels(map[string]string{
		"name":             "kratos",
		"owner":            "helm",
		"status":           "deployed",
		"version":          "1",
		"modifiedAt":       "1785760601",
		formaeManagedLabel: "true",
		"team":             "identity",
	})
	if len(got) != 2 || got[formaeManagedLabel] != "true" || got["team"] != "identity" {
		t.Fatalf("got %v, want only the marker and team", got)
	}
	if withoutSystemLabels(map[string]string{"owner": "helm"}) != nil {
		t.Error("a labels map of only system labels should reduce to nil")
	}
	if withoutSystemLabels(nil) != nil {
		t.Error("nil in, nil out")
	}
}

func TestReleaseLabels_DropsSystemLabelsFromTheForma(t *testing.T) {
	// A forma extracted before the Read-side filter existed still carries them.
	got := releaseLabels(&releaseProperties{Metadata: releaseMetadata{
		Labels: map[string]string{"owner": "helm", "version": "1", "team": "identity"},
	}})
	for _, reserved := range []string{"owner", "version"} {
		if _, present := got[reserved]; present {
			t.Errorf("reserved label %q passed through to Helm", reserved)
		}
	}
	if got["team"] != "identity" || got[formaeManagedLabel] != "true" {
		t.Errorf("got %v, want team plus the marker", got)
	}
}

// Helm's own error for a bare chart name ("non-absolute URLs should be in form
// of repo_name/path_to_chart") does not say what to do. This case is reached by
// adopting a release installed from an HTTP repo, because Helm does not record
// which repository a release came from.
// The split form is what makes `chart` round-trip: it is always the bare chart
// name, which is exactly what Helm records in the release.
func TestResolveChartRef(t *testing.T) {
	// OCI registries have no index, so `helm pull name --repo oci://…` fails.
	// The prefix is joined onto the name and RepoURL is left empty.
	ref, repo := resolveChartRef("podinfo", "oci://ghcr.io/stefanprodan/charts")
	if ref != "oci://ghcr.io/stefanprodan/charts/podinfo" || repo != "" {
		t.Errorf("oci: got ref=%q repo=%q", ref, repo)
	}
	// A trailing slash on the registry must not double up.
	if ref, _ := resolveChartRef("podinfo", "oci://ghcr.io/charts/"); ref != "oci://ghcr.io/charts/podinfo" {
		t.Errorf("trailing slash: got %q", ref)
	}
	// An HTTP repo does have an index, so Helm resolves the bare name against it.
	ref, repo = resolveChartRef("kratos", "https://k8s.ory.sh/helm/charts")
	if ref != "kratos" || repo != "https://k8s.ory.sh/helm/charts" {
		t.Errorf("http: got ref=%q repo=%q", ref, repo)
	}
	// A local path, or a pre-added repo alias, passes through untouched.
	if ref, repo := resolveChartRef("./testdata/charts/hooked", ""); ref != "./testdata/charts/hooked" || repo != "" {
		t.Errorf("local: got ref=%q repo=%q", ref, repo)
	}
}

func TestValidateChartRef(t *testing.T) {
	for _, ok := range []struct{ chart, repo string }{
		{"podinfo", "oci://ghcr.io/stefanprodan/charts"}, // split OCI form
		{"kratos", "https://k8s.ory.sh/helm/charts"},     // split HTTP form
		{"ory/kratos", ""},               // pre-added repo alias
		{"./testdata/charts/hooked", ""}, // local path
	} {
		if err := validateChartRef(ok.chart, ok.repo); err != nil {
			t.Errorf("validateChartRef(%q, %q) rejected a resolvable pair: %v", ok.chart, ok.repo, err)
		}
	}

	// A full OCI reference in `chart` installs fine and then reports the bare name
	// back forever, so it is refused in favour of the split form — and the message
	// must show the exact split to use.
	err := validateChartRef("oci://ghcr.io/stefanprodan/charts/podinfo", "")
	if err == nil {
		t.Fatal("a full OCI reference in chart was accepted")
	}
	for _, want := range []string{`"oci://ghcr.io/stefanprodan/charts"`, `"podinfo"`, "drift"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %s: %v", want, err)
		}
	}

	// A bare name with nowhere to fetch it from.
	err = validateChartRef("podinfo", "")
	if err == nil {
		t.Fatal("a bare chart name with no repoURL was accepted")
	}
	if !strings.Contains(err.Error(), "repoURL") {
		t.Errorf("error does not name the fix: %v", err)
	}
}

func relWithChart(name, version string) *release.Release {
	r := relAt(release.StatusDeployed, 1, time.Minute)
	r.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: name, Version: version}}
	return r
}

// Helm keeps the whole chart in the release record, so re-applying the deployed
// version needs no fetch — and therefore no repoURL. That is what lets an
// extracted forma be adopted without repository information, which Helm cannot
// give us anyway.
func TestStoredChartUsable(t *testing.T) {
	cur := relWithChart("kratos", "0.62.1")

	if !storedChartUsable(cur, &releaseProperties{Chart: "kratos", Version: "0.62.1"}) {
		t.Error("bare name at the deployed version should reuse the stored chart")
	}
	if !storedChartUsable(cur, &releaseProperties{Chart: "ory/kratos", Version: "0.62.1"}) {
		t.Error("repo-qualified name at the deployed version should reuse the stored chart")
	}
	if !storedChartUsable(cur, &releaseProperties{
		Chart: "oci://registry-1.docker.io/charts/kratos", Version: "0.62.1"}) {
		t.Error("oci reference at the deployed version should reuse the stored chart")
	}

	// A different version has to be fetched.
	if storedChartUsable(cur, &releaseProperties{Chart: "kratos", Version: "0.63.0"}) {
		t.Error("a version bump must not reuse the stored chart")
	}
	// A different chart entirely has to be fetched.
	if storedChartUsable(cur, &releaseProperties{Chart: "hydra", Version: "0.62.1"}) {
		t.Error("a different chart must not reuse the stored chart")
	}
	// No version means "newest at apply time"; reusing the stored chart would
	// silently pin the release forever.
	if storedChartUsable(cur, &releaseProperties{Chart: "kratos"}) {
		t.Error("an unpinned version must still be fetched")
	}
	if storedChartUsable(nil, &releaseProperties{Chart: "kratos", Version: "0.62.1"}) {
		t.Error("no current release means nothing to reuse")
	}
	if storedChartUsable(relAt(release.StatusDeployed, 1, time.Minute),
		&releaseProperties{Chart: "kratos", Version: "0.62.1"}) {
		t.Error("a release record without a chart means nothing to reuse")
	}
}

func TestChartRefName(t *testing.T) {
	for in, want := range map[string]string{
		"kratos":     "kratos",
		"ory/kratos": "kratos",
		"oci://registry-1.docker.io/charts/kratos": "kratos",
		"./testdata/charts/hooked":                 "hooked",
	} {
		if got := chartRefName(in); got != want {
			t.Errorf("chartRefName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Read reports `chart` only for a release this plugin does not own, and the two
// halves of that rule fix different bugs.
//
// Owned: Helm records the chart *name*, not the reference, so reporting it would
// answer "podinfo" for a desired "oci://…/podinfo" and every later plain apply
// would be refused as drift. Omitted, formae keeps what the user wrote.
//
// Foreign: there is no desired value to keep, so something must be reported or
// discovery records the release with no chart and `formae extract` emits a forma
// that cannot be applied.
func TestPropertiesFromRelease_ChartOnlyForForeignReleases(t *testing.T) {
	withChart := func(r *release.Release) *release.Release {
		r.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "podinfo", Version: "6.7.1"}}
		return r
	}

	owned := withChart(relAt(release.StatusDeployed, 1, time.Minute)) // relAt marks it owned
	if got := propertiesFromRelease(owned).Chart; got != "" {
		t.Errorf("owned release reported chart %q; it must be omitted so formae keeps the reference", got)
	}

	foreign := withChart(foreignRelAt(release.StatusDeployed, 1))
	if got := propertiesFromRelease(foreign).Chart; got != "podinfo" {
		t.Errorf("foreign release reported chart %q, want %q — adoption needs it", got, "podinfo")
	}

	// version is recoverable either way and must always be reported.
	for name, rel := range map[string]*release.Release{"owned": owned, "foreign": foreign} {
		if got := propertiesFromRelease(rel).Version; got != "6.7.1" {
			t.Errorf("%s release reported version %q, want 6.7.1", name, got)
		}
	}
}

// A Namespace a chart renders must survive the manifest collapse.
//
// Discovery walks namespaced children per *discovered* namespace, so dropping a
// Namespace removes everything inside it from discovery — objects Helm never
// touched included, and any K8S::Helm::Release installed there with them. The
// release standing in for its own namespace is not worth blinding discovery to
// that namespace's contents.
func TestFilterHelmOwned_KeepsNamespaces(t *testing.T) {
	inv := newInventory()
	inv.objects[ObjectID{APIVersion: "v1", Kind: "Namespace", Namespace: "hh-tmplns", Name: "hh-tmplns"}] = "default/nsc"
	inv.byKind[kindRef{Kind: "Namespace", Namespace: "hh-tmplns", Name: "hh-tmplns"}] = "default/nsc"
	inv.byKind[kindRef{Kind: "Namespace", Name: "hh-tmplns"}] = "default/nsc"

	got := FilterHelmOwned(inv, "K8S::Core::Namespace", []string{"hh-tmplns"})

	if len(got) != 1 || got[0] != "hh-tmplns" {
		t.Errorf("a chart-rendered Namespace must stay discoverable, got %v", got)
	}
}
