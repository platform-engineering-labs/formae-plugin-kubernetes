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
	action, target := planSubmit(nil, true, nil, nil)
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
		action, target := planSubmit(relAt(status, 4, time.Minute), false, nil, nil)
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
// A live operation is now proven by the registry rather than guessed from a
// clock: this process is the only place Helm operations for formae run.
func TestPlanSubmit_LivePendingReleaseRetriesRatherThanClaimingProgress(t *testing.T) {
	// Something else in this process is mid-flight for the release, carrying a
	// different desired state than the one being asked for.
	other := liveFlight(opInstall, 2, wantProps())

	for _, status := range []release.Status{
		release.StatusPendingInstall,
		release.StatusPendingUpgrade,
		release.StatusPendingRollback,
	} {
		asked := wantProps()
		asked.Values = map[string]any{"replicaCount": 9.0}

		action, target := planSubmit(relAt(status, 2, 5*time.Second), false, other, asked)
		if action != actionRetry {
			t.Errorf("status %s: got action=%d, want actionRetry", status, action)
		}
		// Must NOT be read as "revision 2 is what we asked for".
		if action == actionUpgrade && target == 2 {
			t.Errorf("status %s: planned an upgrade to the in-flight revision", status)
		}
		// And must never rewrite the record of an operation that is running.
		if action == actionRecover {
			t.Errorf("status %s: planned to recover a release this process is installing", status)
		}
	}
}

func TestPlanSubmit_AbandonedPendingReleaseIsRecoveredNotRetried(t *testing.T) {
	// Nothing is coming back to finish it, so retrying just burns the attempt
	// budget. The lock has to be rewritten before anything can proceed.
	action, _ := planSubmit(relAt(release.StatusPendingInstall, 1, 3*defaultTimeoutSeconds*time.Second), true, nil, nil)
	if action != actionRecover {
		t.Fatalf("got action=%d, want actionRecover", action)
	}
}

// Rewriting a release record is a serious thing to do to somebody else's
// release. An abandoned pending release this plugin did not install is reported,
// never recovered — the Helm CLI and other tools are entitled to their own locks.
func TestPlanSubmit_AbandonedForeignReleaseIsNotRecovered(t *testing.T) {
	foreign := foreignRelAt(release.StatusPendingInstall, 1)
	foreign.Info.LastDeployed = helmtime.Time{Time: time.Now().Add(-3 * defaultTimeoutSeconds * time.Second)}

	action, _ := planSubmit(foreign, true, nil, nil)
	if action == actionRecover {
		t.Fatal("planned to rewrite the record of a release formae did not install")
	}
	// Reported instead, once it is stale enough that saying something is more
	// use than saying nothing.
	if action != actionBlocked {
		t.Fatalf("got action=%d, want actionBlocked", action)
	}
}

// A foreign release that has only just gone pending belongs to an operation that
// is very likely still running somewhere this plugin cannot see.
func TestPlanSubmit_RecentForeignPendingReleaseIsRetried(t *testing.T) {
	action, _ := planSubmit(foreignRelAt(release.StatusPendingInstall, 1), true, nil, nil)
	if action != actionRetry {
		t.Fatalf("got action=%d, want actionRetry", action)
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
	if stalled(pendingSince(time.Minute), nil) {
		t.Error("a one-minute-old pending release was called stalled")
	}
	if !stalled(pendingSince(3*defaultTimeoutSeconds*time.Second), nil) {
		t.Error("a long-abandoned pending release was not called stalled")
	}
	if stalled(&release.Release{Info: &release.Info{Status: release.StatusPendingInstall}}, nil) {
		t.Error("a release with no timestamp was called stalled")
	}
}

// The window has to come from the operation's own timeout, not the package
// default. A chart given timeoutSeconds=1800 was previously declared dead at
// 1200s — while its own install was still legitimately running — and the
// operator was told to `helm uninstall` a healthy release.
func TestStalled_HonoursTheTimeoutRecordedOnTheRelease(t *testing.T) {
	pendingSince := func(d time.Duration, labels map[string]string) *release.Release {
		return &release.Release{
			Labels: labels,
			Info: &release.Info{
				Status:       release.StatusPendingInstall,
				LastDeployed: helmtime.Time{Time: time.Now().Add(-d)},
			},
		}
	}

	longTimeout := map[string]string{formaeTimeoutLabel: "1800"}
	if stalled(pendingSince(50*time.Minute, longTimeout), nil) {
		t.Error("a release inside twice its own 1800s timeout was called stalled")
	}
	if !stalled(pendingSince(61*time.Minute, longTimeout), nil) {
		t.Error("a release past twice its own 1800s timeout was not called stalled")
	}

	// No label: releases installed before this existed, and every foreign
	// release. The package default still applies.
	if !stalled(pendingSince(3*defaultTimeoutSeconds*time.Second, nil), nil) {
		t.Error("an unlabelled long-abandoned release was not called stalled")
	}
	// A label we cannot parse must not be trusted into an unbounded window.
	if !stalled(pendingSince(3*defaultTimeoutSeconds*time.Second, map[string]string{formaeTimeoutLabel: "soon"}), nil) {
		t.Error("an unparseable timeout label suppressed the default window")
	}
}

// The registry may only ever suppress a stall verdict. A live flight proves
// this process is still working on the release, whatever Helm's record says.
func TestStalled_NotStalledWhileThisProcessIsRunningIt(t *testing.T) {
	rel := &release.Release{
		Name:      "app",
		Namespace: "ns",
		Info: &release.Info{
			Status:       release.StatusPendingInstall,
			LastDeployed: helmtime.Time{Time: time.Now().Add(-3 * defaultTimeoutSeconds * time.Second)},
		},
	}

	// With no flight this is long past the window. A miss proves only that this
	// process is not running it — the Helm CLI and a second agent both exist —
	// so the conservative verdict has to stand.
	if !stalled(rel, nil) {
		t.Fatal("precondition: the release should read as stalled with no flight")
	}

	if stalled(rel, &inflight{deadline: time.Now().Add(time.Hour)}) {
		t.Error("a release this process is actively installing was called stalled")
	}

	// A flight past its own deadline is unwinding, not working, so it must stop
	// suppressing the verdict.
	if !stalled(rel, &inflight{deadline: time.Now().Add(-time.Second)}) {
		t.Error("an expired flight still suppressed the stall verdict")
	}
}

func TestReleaseLabels_CarriesTheOperationTimeout(t *testing.T) {
	labels := releaseLabels(&releaseProperties{TimeoutSeconds: 900})
	if got := labels[formaeTimeoutLabel]; got != "900" {
		t.Errorf("timeout label = %q, want 900", got)
	}
	// Unset means the default, and Status has to be able to read it back rather
	// than guess.
	labels = releaseLabels(&releaseProperties{})
	if got := labels[formaeTimeoutLabel]; got != "600" {
		t.Errorf("default timeout label = %q, want 600", got)
	}
	if labels[formaeManagedLabel] != "true" {
		t.Error("the ownership marker was lost")
	}
}

// The plugin's own bookkeeping labels must not reach the resource shape. They
// are not in the user's forma, so reporting them makes every later plain apply
// look like drift, and `formae extract` writes them into the generated file.
func TestPropertiesFromRelease_HidesFormaeBookkeepingLabels(t *testing.T) {
	rel := relWithChart("podinfo", "6.7.1")
	rel.Labels = map[string]string{
		formaeManagedLabel: "true",
		formaeTimeoutLabel: "900",
		"team":             "platform",
	}

	got := propertiesFromRelease(rel).Metadata.Labels
	if _, ok := got[formaeManagedLabel]; ok {
		t.Error("the ownership marker leaked into the reported labels")
	}
	if _, ok := got[formaeTimeoutLabel]; ok {
		t.Error("the timeout label leaked into the reported labels")
	}
	if got["team"] != "platform" {
		t.Errorf("user labels = %v, want team=platform preserved", got)
	}
}

// A release carrying nothing but our bookkeeping must report no labels at all,
// not an empty map that reads as a change against a forma with none.
func TestPropertiesFromRelease_OmitsLabelsWhenOnlyBookkeepingRemains(t *testing.T) {
	rel := relWithChart("podinfo", "6.7.1")
	rel.Labels = map[string]string{formaeManagedLabel: "true", formaeTimeoutLabel: "600"}

	if got := propertiesFromRelease(rel).Metadata.Labels; got != nil {
		t.Errorf("labels = %v, want nil", got)
	}
}

// The adoption guard. Create means formae believes the release does not exist,
// so a settled release it did not install is someone else's — taking it over
// would rewrite that release's history and make `helm rollback` roll back to
// revisions this forma never described.
func TestPlanSubmit_ForeignReleaseIsRefusedOnCreate(t *testing.T) {
	action, _ := planSubmit(foreignRelAt(release.StatusDeployed, 7), true, nil, nil)
	if action != actionForeign {
		t.Fatalf("got action=%d, want actionForeign", action)
	}
}

// Once adopted, formae holds a NativeID, so the operation arrives as Update and
// the guard must stand aside — otherwise adoption could never complete.
func TestPlanSubmit_ForeignReleaseIsUpgradableOnUpdate(t *testing.T) {
	action, target := planSubmit(foreignRelAt(release.StatusDeployed, 7), false, nil, nil)
	if action != actionUpgrade || target != 8 {
		t.Fatalf("got action=%d target=%d, want upgrade/8", action, target)
	}
}

// Create withholds the NativeID until the release is deployed, so a failed first
// install is retried as a Create. Our own marker is what lets that retry through
// instead of being mistaken for a foreign release.
func TestPlanSubmit_OwnFailedInstallIsRetakenOnCreate(t *testing.T) {
	action, target := planSubmit(relAt(release.StatusFailed, 1, time.Minute), true, nil, nil)
	if action != actionUpgrade || target != 2 {
		t.Fatalf("got action=%d target=%d, want upgrade/2", action, target)
	}
}

// ---------------------------------------------------------------------------
// Surviving a restart
// ---------------------------------------------------------------------------

func wantProps() *releaseProperties {
	return &releaseProperties{
		Metadata: releaseMetadata{Name: "app", Namespace: "ns"},
		Chart:    "podinfo",
		Version:  "6.7.1",
		Values:   map[string]any{"replicaCount": 2.0},
	}
}

func liveFlight(op opKind, revision int, want *releaseProperties) *inflight {
	return &inflight{
		op:          op,
		revision:    revision,
		fingerprint: fingerprint(want),
		started:     time.Now(),
		deadline:    time.Now().Add(10 * time.Minute),
	}
}

// The agent does not resume an interrupted operation, it re-drives it:
// ReRunIncompleteCommands resets an InProgress resource update to NotStarted and
// calls Create again (metastructure.go:1262). The plugin is a separate process,
// so our install goroutine is usually still running. Without rejoin the
// re-driven call sees Helm's lock, returns ResourceConflict until its attempts
// run out, and fails a command whose install then succeeds anyway.
func TestPlanSubmit_RejoinsItsOwnInFlightOperation(t *testing.T) {
	want := wantProps()
	cur := relAt(release.StatusPendingInstall, 1, 5*time.Second)

	action, target := planSubmit(cur, true, liveFlight(opInstall, 1, want), want)
	if action != actionRejoin {
		t.Fatalf("got action=%d, want actionRejoin", action)
	}
	// Rejoining must name the revision already in flight — that is the whole
	// point, the RequestID has to reconstruct identically.
	if target != 1 {
		t.Errorf("target revision = %d, want 1", target)
	}
}

// The narrow reading of the rule at release.go:374-380. Reporting InProgress for
// an in-flight revision that is NOT the one we were asked for would report
// success for work that never ran. The fingerprint is what separates the two.
func TestPlanSubmit_DoesNotRejoinADifferentDesiredState(t *testing.T) {
	inFlight := wantProps()
	asked := wantProps()
	asked.Values = map[string]any{"replicaCount": 5.0}

	action, _ := planSubmit(
		relAt(release.StatusPendingInstall, 1, 5*time.Second), true,
		liveFlight(opInstall, 1, inFlight), asked)
	if action != actionRetry {
		t.Fatalf("got action=%d, want actionRetry", action)
	}
}

// A registry miss on a release this plugin installed is not ambiguity, it is an
// answer: Helm operations for formae run in exactly one process, so nothing is
// driving this release and its pending record is a lock nobody will release.
// There is nothing to rejoin and nothing to wait for.
func TestPlanSubmit_OwnPendingReleaseWithNoFlightIsRecovered(t *testing.T) {
	want := wantProps()
	action, target := planSubmit(relAt(release.StatusPendingInstall, 1, 5*time.Second), true, nil, want)
	if action != actionRecover {
		t.Fatalf("got action=%d, want actionRecover", action)
	}
	if target != 1 {
		t.Errorf("target = %d, want the pending revision 1", target)
	}
}

// Recovery must not wait out a clock. The whole point of deciding from the
// registry is that a release abandoned seconds ago is as recoverable as one
// abandoned an hour ago.
func TestPlanSubmit_RecoveryDoesNotWaitOutTheStallWindow(t *testing.T) {
	fresh := relAt(release.StatusPendingInstall, 1, time.Second)
	if action, _ := planSubmit(fresh, true, nil, wantProps()); action != actionRecover {
		t.Fatalf("got action=%d, want actionRecover for a release abandoned one second ago", action)
	}
}

// A flight past its deadline is a goroutine that has already given up; its
// context is cancelled and it is not going to finish the operation.
func TestPlanSubmit_DoesNotRejoinAnExpiredFlight(t *testing.T) {
	want := wantProps()
	expired := liveFlight(opInstall, 1, want)
	expired.deadline = time.Now().Add(-time.Second)

	action, _ := planSubmit(relAt(release.StatusPendingInstall, 1, 5*time.Second), true, expired, want)
	if action != actionRetry {
		t.Fatalf("got action=%d, want actionRetry", action)
	}
}

// A flight for a different revision is not this operation.
func TestPlanSubmit_DoesNotRejoinAnotherRevision(t *testing.T) {
	want := wantProps()
	action, _ := planSubmit(
		relAt(release.StatusPendingUpgrade, 4, 5*time.Second), false,
		liveFlight(opUpgrade, 2, want), want)
	if action != actionRetry {
		t.Fatalf("got action=%d, want actionRetry", action)
	}
}

// The second half of the restart problem. If the agent dies after the install
// finished, it still re-drives Create — and without this the release is upgraded
// to revision 2 for nothing, re-firing pre-upgrade and post-upgrade hooks. On
// kratos that is a second automigrate run.
func TestPlanSubmit_AlreadyDeployedDesiredStateIsANoOp(t *testing.T) {
	want := wantProps()
	cur := relWithChart("podinfo", "6.7.1")
	cur.Config = map[string]any{"replicaCount": 2.0}

	for _, isCreate := range []bool{true, false} {
		action, target := planSubmit(cur, isCreate, nil, want)
		if action != actionSettled {
			t.Errorf("isCreate=%v: got action=%d, want actionSettled", isCreate, action)
		}
		if target != 1 {
			t.Errorf("isCreate=%v: target = %d, want the live revision 1", isCreate, target)
		}
	}
}

// Concluding "settled" wrongly silently drops a change, so every doubt has to
// fall through to a real upgrade.
func TestPlanSubmit_NotSettledOnAnyDifference(t *testing.T) {
	cases := map[string]struct {
		mutateCurrent func(*release.Release)
		mutateWant    func(*releaseProperties)
	}{
		"different values": {
			mutateWant: func(p *releaseProperties) { p.Values = map[string]any{"replicaCount": 3.0} },
		},
		"extra desired value": {
			mutateWant: func(p *releaseProperties) {
				p.Values = map[string]any{"replicaCount": 2.0, "ingress": true}
			},
		},
		"different chart version": {
			mutateWant: func(p *releaseProperties) { p.Version = "6.7.2" },
		},
		// "newest at apply time" can never be concluded settled — the same
		// reasoning storedChartUsable already applies.
		"unpinned version": {
			mutateWant: func(p *releaseProperties) { p.Version = "" },
		},
		"different chart": {
			mutateWant: func(p *releaseProperties) { p.Chart = "other" },
		},
		"not deployed": {
			mutateCurrent: func(r *release.Release) { r.Info.Status = release.StatusFailed },
		},
		"not owned by formae": {
			mutateCurrent: func(r *release.Release) { r.Labels = map[string]string{"someone": "else"} },
		},
	}

	for name, tc := range cases {
		cur := relWithChart("podinfo", "6.7.1")
		cur.Config = map[string]any{"replicaCount": 2.0}
		want := wantProps()
		if tc.mutateCurrent != nil {
			tc.mutateCurrent(cur)
		}
		if tc.mutateWant != nil {
			tc.mutateWant(want)
		}

		action, _ := planSubmit(cur, false, nil, want)
		if action == actionSettled {
			t.Errorf("%s: planned actionSettled, which would drop the change", name)
		}
	}
}

// Values are compared as canonical JSON, so map ordering and the int/float
// asymmetry between a decoded release record and a decoded request cannot
// produce a spurious upgrade.
func TestPlanSubmit_SettledIgnoresValueEncodingNoise(t *testing.T) {
	cur := relWithChart("podinfo", "6.7.1")
	cur.Config = map[string]any{"b": 2, "a": map[string]any{"d": 4, "c": 3}}

	want := wantProps()
	want.Values = map[string]any{"a": map[string]any{"c": 3.0, "d": 4.0}, "b": 2.0}

	if action, _ := planSubmit(cur, false, nil, want); action != actionSettled {
		t.Errorf("got action=%d, want actionSettled", action)
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

// No label that came from bookkeeping may reach a forma or come back to Helm —
// only ones the user actually declared.
//
// Helm's own: the secrets driver filters them on Get but not on the list/last
// paths (driver/secrets.go:103,141), so an extracted forma picked them up and
// Helm then rejected the next upgrade outright.
//
// Ours: the marker is stamped unconditionally, so reporting it back out of Read
// diffs against every forma that declares labels. releaseLabels re-adds it, so
// dropping it here costs nothing on the way in — see
// TestReleaseLabels_StampsTheMarker and
// TestPropertiesFromRelease_OmitsTheOwnershipMarker for the two directions.
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
	if len(got) != 1 || got["team"] != "identity" {
		t.Fatalf("got %v, want only team", got)
	}
	if withoutSystemLabels(map[string]string{"owner": "helm"}) != nil {
		t.Error("a labels map of only system labels should reduce to nil")
	}
	if withoutSystemLabels(map[string]string{formaeManagedLabel: "true"}) != nil {
		t.Error("a labels map of only the marker should reduce to nil")
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
