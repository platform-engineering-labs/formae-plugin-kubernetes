//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// What happens to a Helm release when the thing driving it dies.
//
// The agent does not resume an interrupted operation, it re-drives it:
// ReRunIncompleteCommands resets an InProgress resource update to NotStarted and
// calls Create again from scratch, discarding the RequestID (formae
// internal/metastructure/metastructure.go:1262). The plugin is a separate
// process, so an agent restart leaves this plugin's install goroutine running
// and hands it a duplicate Create. These tests reproduce that directly, plus the
// mirror case where the plugin is the one being stopped.
package helm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// slowProps builds a request whose pre-install hook takes long enough that the
// install is reliably still in flight when the test acts on it.
func slowProps(t *testing.T, name, chart string, hookSeconds int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": name, "namespace": testNamespace},
		"chart":    chart,
		"values":   map[string]any{"message": "hello", "hookSleepSeconds": hookSeconds},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func currentRelease(t *testing.T, r *Release, name string) *release.Release {
	t.Helper()
	conf, err := newActionConfig(r.Config, testNamespace)
	if err != nil {
		t.Fatalf("newActionConfig: %v", err)
	}
	rel, err := lastRelease(conf, name)
	if err != nil {
		t.Fatalf("lastRelease: %v", err)
	}
	return rel
}

// purgeRelease removes the release and waits for it to actually be gone.
//
// Delete is asynchronous — it returns InProgress and Helm purges the record only
// after WaitForDelete and the post-delete hooks — so a test that does not wait
// leaves a deployed release behind for the next run, which then plans an upgrade
// instead of the install the test is about.
func purgeRelease(t *testing.T, r *Release, name string) {
	t.Helper()
	// Unwind anything still installing first. Uninstalling underneath a live
	// install races it: the goroutine goes on to write the record we just
	// purged. Tests run sequentially, so draining everything is safe here.
	DrainInFlight(30 * time.Second)
	removeFlight(r.Config, testNamespace, name)

	if _, err := r.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     prov.NativeID(testNamespace, name),
		ResourceType: ResourceTypeRelease,
	}); err != nil {
		t.Logf("purge %s: %v", name, err)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if currentRelease(t, r, name) == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("release %s still present after purge; later runs will not start clean", name)
}

func setUpRelease(t *testing.T, name string) (*Release, string) {
	t.Helper()
	r, _ := newTestRelease(t)
	ensureNamespace(t, r.Client.CoreV1().Namespaces())
	// Start clean as well as finish clean: a run killed mid-test leaves a
	// release the next run would otherwise upgrade rather than install.
	purgeRelease(t, r, name)
	t.Cleanup(func() { purgeRelease(t, r, name) })
	return r, chartPath(t)
}

// Matrix row 1: the agent dies while the plugin carries on installing.
//
// Before rejoin the duplicate Create met Helm's pending lock, returned
// ResourceConflict until the agent ran out of attempts, and failed a command
// whose install then went on to succeed — leaving a deployed release that formae
// held no NativeID for.
func TestReDrivenCreateRejoinsTheInstallAlreadyInFlight(t *testing.T) {
	const name = "rejoin"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	first, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 45),
	})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("first Create returned %s, want InProgress", first.ProgressResult.OperationStatus)
	}

	// Exactly what the agent does on restart: same desired state, no memory of
	// the RequestID it was given.
	second, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 45),
	})
	if err != nil {
		t.Fatalf("re-driven Create errored instead of rejoining: %v", err)
	}
	if second.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("re-driven Create returned %s (%s), want InProgress",
			second.ProgressResult.OperationStatus, second.ProgressResult.StatusMessage)
	}
	// requestID is a pure function of namespace, name, revision and op, so
	// rejoining necessarily reconstructs the handle the interrupted call held.
	if second.ProgressResult.RequestID != first.ProgressResult.RequestID {
		t.Errorf("re-driven RequestID = %q, want the in-flight %q",
			second.ProgressResult.RequestID, first.ProgressResult.RequestID)
	}
	t.Logf("rejoin message: %s", second.ProgressResult.StatusMessage)

	// One Helm operation ran, not two.
	if rel := currentRelease(t, r, name); rel == nil || rel.Version != 1 {
		t.Fatalf("release version = %v, want a single revision 1", rel)
	}

	final := pollUntilTerminal(t, r, "", second.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("rejoined install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	if final.NativeID != prov.NativeID(testNamespace, name) {
		t.Errorf("NativeID on success = %q, want %q", final.NativeID, prov.NativeID(testNamespace, name))
	}
	if rel := currentRelease(t, r, name); rel.Version != 1 {
		t.Errorf("revision = %d after a rejoined install, want 1", rel.Version)
	}
}

// A re-driven Create carrying a DIFFERENT desired state is not our operation,
// and must still take the conservative path. Rejoining it would report success
// for a change that never ran.
func TestReDrivenCreateWithADifferentDesiredStateStillConflicts(t *testing.T) {
	const name = "no-rejoin"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	if _, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 45),
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	changed, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 46),
	})
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if changed.ProgressResult.OperationStatus != resource.OperationStatusFailure ||
		changed.ProgressResult.ErrorCode != resource.OperationErrorCodeResourceConflict {
		t.Errorf("got %s/%s, want Failure/ResourceConflict",
			changed.ProgressResult.OperationStatus, changed.ProgressResult.ErrorCode)
	}
}

// Matrix row 2: the agent dies after the install finished, then re-drives Create
// against a release that is already exactly right.
//
// Without actionSettled this planned an upgrade — bumping the revision for
// nothing and re-running the chart's pre-upgrade and post-upgrade hooks. On a
// chart like kratos that is a second database migration.
func TestReDrivenCreateOnASettledReleaseRunsNoHelmOperation(t *testing.T) {
	const name = "settled"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	// Pin the version, because "newest at apply time" can never be concluded
	// settled. The test chart's version is what the record will carry.
	desired := slowProps(t, name, chart, 0)
	var asMap map[string]any
	if err := json.Unmarshal(desired, &asMap); err != nil {
		t.Fatal(err)
	}

	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   desired,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	installed := currentRelease(t, r, name)
	if installed.Version != 1 {
		t.Fatalf("precondition: revision = %d, want 1", installed.Version)
	}
	// Re-drive with the version the release actually landed on, which is what an
	// extracted or already-reconciled forma carries.
	asMap["version"] = installed.Chart.Metadata.Version
	pinned, err := json.Marshal(asMap)
	if err != nil {
		t.Fatal(err)
	}

	// Everything gone from this namespace would mean the hook re-ran; capture
	// what a fresh hook Job would look like.
	before := time.Now()

	redriven, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   pinned,
	})
	if err != nil {
		t.Fatalf("re-driven Create on a settled release errored: %v", err)
	}
	t.Logf("settled message: %s", redriven.ProgressResult.StatusMessage)

	final := pollUntilTerminal(t, r, "", redriven.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("re-driven Create ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	// The whole point: no second Helm operation.
	if rel := currentRelease(t, r, name); rel.Version != 1 {
		t.Errorf("revision = %d after re-driving a settled release, want 1", rel.Version)
	}
	// And therefore no second hook run.
	jobs, err := r.Client.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range jobs.Items {
		if j.Name == name+"-preinstall" && j.CreationTimestamp.After(before) {
			t.Errorf("hook Job %s re-ran on a settled release", j.Name)
		}
	}
}

// Matrix row 3: the plugin is the one being stopped.
//
// This is the whole thesis of the drain. Cancelling makes Helm run failRelease
// (install.go:411), which records `failed` — a state the next apply upgrades
// over. Being killed without cancelling leaves `pending-install`, which Helm
// refuses to install OR upgrade and which only `helm uninstall` clears.
func TestDrainLeavesTheReleaseFailedRatherThanPending(t *testing.T) {
	const name = "drained"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	if _, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 60),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rel := currentRelease(t, r, name); rel == nil || !releaseIsPending(rel) {
		t.Fatalf("precondition: release is %v, want pending", rel)
	}

	if !DrainInFlight(30 * time.Second) {
		t.Fatal("drain did not unwind the install")
	}

	rel := currentRelease(t, r, name)
	if rel == nil || rel.Info == nil {
		t.Fatal("release record vanished after the drain")
	}
	if rel.Info.Status == release.StatusPendingInstall {
		t.Fatal("release left pending-install after a graceful drain; " +
			"recovery would need `helm uninstall`")
	}
	if rel.Info.Status != release.StatusFailed {
		t.Fatalf("release status = %s, want failed", rel.Info.Status)
	}

	// A failed release is recoverable without an operator: the next apply plans
	// an upgrade over it, which is the entire benefit of the drain.
	action, _ := planSubmit(rel, true, nil, nil)
	if action != actionUpgrade {
		t.Fatalf("planned action %d over a failed release, want actionUpgrade", action)
	}

	retried, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 0),
	})
	if err != nil {
		t.Fatalf("retry after drain: %v", err)
	}
	final := pollUntilTerminal(t, r, "", retried.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("retry after drain ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
}

// wedge rewrites a release's record back to pending-install, dated far enough in
// the past to be past its stall window.
//
// This is what a SIGKILLed plugin leaves behind, reproduced deterministically.
// Racing a real kill against Helm's final record write is a window of
// milliseconds, and the two sides of that window need different recoveries, so
// they are worth testing on purpose rather than by luck.
func wedge(t *testing.T, r *Release, name string) {
	t.Helper()
	conf, err := newActionConfig(r.Config, testNamespace)
	if err != nil {
		t.Fatalf("newActionConfig: %v", err)
	}
	rel, err := lastRelease(conf, name)
	if err != nil || rel == nil {
		t.Fatalf("lastRelease: %v", err)
	}
	rel.Info.Status = release.StatusPendingInstall
	rel.Info.LastDeployed = helmtime.Time{Time: time.Now().Add(-3 * defaultTimeoutSeconds * time.Second)}
	if err := conf.Releases.Update(rel); err != nil {
		t.Fatalf("wedge release: %v", err)
	}
}

// The case that matters most, because getting it wrong is expensive rather than
// merely slow.
//
// Helm writes `deployed` LAST, after the hooks have run and every object exists.
// A plugin killed in that final moment leaves a record indistinguishable from
// one killed before it did anything — but the cluster is fully installed. Marking
// that `failed` and upgrading over it would re-run pre-upgrade hooks, which on a
// chart like kratos means running a database migration a second time to reach a
// state that had already been reached.
func TestStatusRecoversAReleaseWhoseFinalRecordWriteWasLost(t *testing.T) {
	const name = "lost-write"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 0),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	hookBefore, hadHook := hookCreatedAt(t, r, name)

	// Everything is installed and ready; only the record is a lie.
	wedge(t, r, name)

	res, err := r.Status(ctx, &resource.StatusRequest{
		RequestID:    requestID(testNamespace, name, 1, opInstall),
		ResourceType: ResourceTypeRelease,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("Status returned %s (%s); a fully installed release was not recognised",
			res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}

	rel := currentRelease(t, r, name)
	if rel.Info.Status != release.StatusDeployed {
		t.Errorf("record left %s, want deployed", rel.Info.Status)
	}
	if rel.Version != 1 {
		t.Errorf("revision = %d, want 1 — recovery ran a Helm operation it did not need to", rel.Version)
	}
	if hadHook {
		if after, still := hookCreatedAt(t, r, name); still && after.After(hookBefore) {
			t.Error("recovery re-ran the pre-install hook on an already-installed release")
		}
	}
}

// The other side of that window: killed before the objects were made. Here the
// record must go to `failed`, which an upgrade is allowed to run over, and the
// error has to be recoverable so the agent re-drives rather than handing the
// release to an operator.
func TestStatusMarksAnIncompleteAbandonedReleaseFailed(t *testing.T) {
	const name = "incomplete"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 0),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	// Remove an object the chart renders, so the release is genuinely incomplete.
	if err := r.Client.CoreV1().ConfigMaps(testNamespace).Delete(ctx,
		name+"-config", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete chart ConfigMap: %v", err)
	}
	wedge(t, r, name)

	res, err := r.Status(ctx, &resource.StatusRequest{
		RequestID:    requestID(testNamespace, name, 1, opInstall),
		ResourceType: ResourceTypeRelease,
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusFailure {
		t.Fatalf("Status returned %s, want Failure", res.ProgressResult.OperationStatus)
	}
	// Recoverable, or the agent stops instead of re-driving into the upgrade
	// that finishes the job.
	if !resource.IsRecoverable(res.ProgressResult.ErrorCode) {
		t.Errorf("error code %q is not recoverable; the agent will not retry",
			res.ProgressResult.ErrorCode)
	}
	if rel := currentRelease(t, r, name); rel.Info.Status != release.StatusFailed {
		t.Errorf("record left %s, want failed", rel.Info.Status)
	}

	// And the promise that makes it safe to mark failed: an upgrade runs over it
	// and restores what was missing, with no uninstall.
	retried, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   slowProps(t, name, chart, 0),
	})
	if err != nil {
		t.Fatalf("re-apply after recovery: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", retried.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("upgrade over the recovered release ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	if _, err := r.Client.CoreV1().ConfigMaps(testNamespace).Get(ctx, name+"-config", metav1.GetOptions{}); err != nil {
		t.Errorf("chart ConfigMap was not restored by the recovery upgrade: %v", err)
	}
}

// hookCreatedAt reports when the chart's pre-install hook Job was created.
func hookCreatedAt(t *testing.T, r *Release, name string) (time.Time, bool) {
	t.Helper()
	job, err := r.Client.BatchV1().Jobs(testNamespace).Get(
		context.Background(), name+"-preinstall", metav1.GetOptions{})
	if err != nil {
		return time.Time{}, false
	}
	return job.CreationTimestamp.Time, true
}

// The timeout has to survive into the cluster, because Status is handed a
// RequestID and nothing else — it has no access to the forma.
func TestTimeoutIsRecoverableFromTheReleaseRecord(t *testing.T) {
	const name = "timeout-label"
	r, chart := setUpRelease(t, name)
	ctx := context.Background()

	raw, err := json.Marshal(map[string]any{
		"metadata":       map[string]any{"name": name, "namespace": testNamespace},
		"chart":          chart,
		"values":         map[string]any{"message": "hello", "hookSleepSeconds": 0},
		"timeoutSeconds": 1800,
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := r.Create(ctx, &resource.CreateRequest{ResourceType: ResourceTypeRelease, Properties: raw})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	rel := currentRelease(t, r, name)
	if got := releaseTimeout(rel); got != 1800*time.Second {
		t.Errorf("recovered timeout = %s, want 30m", got)
	}

	// And it must not leak back out as part of the resource, or every later
	// plain apply reads as drift.
	read, err := r.Read(ctx, &resource.ReadRequest{
		NativeID:     prov.NativeID(testNamespace, name),
		ResourceType: ResourceTypeRelease,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var props releaseProperties
	if err := json.Unmarshal([]byte(read.Properties), &props); err != nil {
		t.Fatal(err)
	}
	for key := range props.Metadata.Labels {
		if key == formaeTimeoutLabel || key == formaeManagedLabel {
			t.Errorf("bookkeeping label %q leaked into the reported resource", key)
		}
	}
}
