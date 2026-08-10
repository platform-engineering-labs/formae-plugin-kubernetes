//go:build stability

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// The four ways a Helm install gets interrupted, run against real processes.
//
// Helm runs inside the plugin process, and the agent does not resume an
// interrupted operation — it re-drives it. ReRunIncompleteCommands resets an
// InProgress resource update to NotStarted and calls Create again from scratch,
// discarding the RequestID (formae internal/metastructure/metastructure.go:1262).
// Because the plugin is a separate process, an agent restart usually leaves the
// install goroutine running and hands it a duplicate Create.
//
// Each test below kills something for real and then asks two questions: what did
// formae report, and what is actually in the cluster. Those answers disagreeing
// is the failure this suite exists to catch.
package helmstability

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hookSeconds is how long the chart's pre-install hook blocks. It has to outlast
// the time it takes to stop and restart an agent, or the scenario finishes
// before the test can interrupt it.
const hookSeconds = 90

// installTimeout bounds a command that is expected to succeed.
const installTimeout = 6 * time.Minute

// ---------------------------------------------------------------------------
// Row 1: the agent dies mid-install, the plugin carries on
// ---------------------------------------------------------------------------

// The agent is SIGKILLed, not stopped. That distinction is measured, not
// stylistic: `formae agent stop` takes the plugin down with it, so the install
// dies too. Only a crash can leave one running.
//
// Whether it actually does is a platform property, and this test runs on both
// sides of it rather than picking one. formae sets Pdeathsig on Linux and
// nothing on macOS (procattr_linux.go / procattr_other.go), so:
//
//	Linux  the kernel kills the plugin with the agent; the install dies
//	       mid-flight and the release is left wedged at pending-install
//	macOS  the plugin is orphaned onto pid 1 and finishes the install
//
// Two different routes, one invariant, and the invariant is the point: the
// command converges to a deployed release with no operator involved. What
// differs is only which mechanism gets it there — recovery-then-upgrade when
// the install died, actionSettled when it finished — and the test reads which
// happened off the machine instead of off runtime.GOOS, so it keeps telling the
// truth if the platform behaviour ever changes.
//
// Note what cannot save either case: a restarted agent never reattaches to a
// surviving plugin. It spawns a fresh one, whose in-flight registry is empty, so
// rejoining is unreachable across an agent restart on every platform.
func TestAgentCrashMidInstall(t *testing.T) {
	s := newScenario(t, "agent-mid")
	forma := s.writeForma("forma.pkl", "hello", hookSeconds, 600)

	commandID := submitApply(t, forma)
	t.Logf("submitted command %s", commandID)

	// Wait until Helm has written the record, so there is a real in-flight
	// operation to interrupt rather than a race with chart rendering.
	waitForReleaseStatus(t, s.namespace, s.release, "pending-install", 2*time.Minute)

	t.Log("SIGKILL to the agent mid-install")
	orphan, pluginSurvived := crashAgent(t)
	if pluginSurvived {
		t.Logf("plugin %d outlived the agent and is still installing (no Pdeathsig here)", orphan)
	} else {
		t.Log("plugin died with the agent (Pdeathsig); the release is wedged mid-install")
	}

	// Reap before restarting, which is the cleanup Linux gets from Pdeathsig for
	// free. Skipping it does not merely leave a stray process: the orphan holds
	// its Ergo node name, so the replacement plugin the new agent spawns cannot
	// register, the supervisor exhausts MaxPluginRestarts, and the namespace is
	// left with no plugin at all. That is a failure of the harness, not of the
	// recovery under test, and it is what made this scenario look broken before.
	if reaped := reapOrphanedPlugins(t); reaped > 0 {
		t.Logf("reaped %d orphaned plugin(s) so the agent can respawn them", reaped)
	}

	if err := launchAgent(); err != nil {
		t.Fatalf("restart agent after crash: %v", err)
	}
	waitForPlugin(t)

	// Invariant 1: the command reaches a verdict. Whether that verdict is
	// success depends on how much of the install survived — killing the plugin
	// mid-hook genuinely leaves nothing installed — but it must never sit in
	// InProgress reporting work that nothing is doing.
	outcome := waitCommand(t, commandID, installTimeout)
	t.Logf("command ended %s: %s", outcome.State, outcome.Message)

	// Invariant 2: the release does not hold a Helm lock. This is the one that
	// decides whether a crash costs somebody manual recovery.
	rel := currentRelease(t, s.namespace, s.release)
	if rel.Found && (strings.HasPrefix(rel.Status, "pending-") || rel.Status == "uninstalling") {
		t.Fatalf("release left stuck in %q — Helm refuses install and upgrade in this state", rel.Status)
	}

	// A failed command has to say why, or whoever is on call has nothing to act
	// on and no reason to believe re-applying will help.
	if outcome.State != "Success" && outcome.Message == "" {
		t.Error("command failed with no diagnostic")
	}

	// Invariant 3: re-applying converges, with no operator and no uninstall.
	// This is what makes a crash survivable rather than merely reported.
	recovery := s.writeForma("recovery.pkl", "hello", 0, 600)
	if again := waitApply(t, submitApply(t, recovery), installTimeout); again.State != "Success" {
		t.Fatalf("re-apply after an agent crash ended %s: %s", again.State, again.Message)
	}

	final := currentRelease(t, s.namespace, s.release)
	if final.Status != "deployed" {
		t.Errorf("status = %q after re-apply, want deployed", final.Status)
	}
	t.Logf("converged to %s at revision %d", final.Status, final.Revision)

	// And the route taken, recorded rather than asserted: which one happens is a
	// platform property, and both are legitimate.
	if pluginSurvived && outcome.State == "Success" && final.Revision == 1 {
		t.Log("the orphan completed the install and recovery recognised it — no redundant Helm operation")
	}
}

// ---------------------------------------------------------------------------
// Row 2: the agent dies after the install finished
// ---------------------------------------------------------------------------

// The re-driven Create arrives against a release that is already exactly right.
// Without actionSettled it planned an upgrade: a revision bump for nothing, and
// the chart's pre-upgrade and post-upgrade hooks fired again. On a chart like
// kratos that is a second database migration.
func TestAgentRestartAfterInstall(t *testing.T) {
	s := newScenario(t, "agent-after")
	// Fast hook: this scenario is about what happens once the install is done.
	forma := s.writeForma("forma.pkl", "hello", 0, 600)

	if outcome := waitApply(t, submitApply(t, forma), installTimeout); outcome.State != "Success" {
		t.Fatalf("initial install ended %s: %s", outcome.State, outcome.Message)
	}
	installed := currentRelease(t, s.namespace, s.release)
	if installed.Revision != 1 {
		t.Fatalf("precondition: revision = %d, want 1", installed.Revision)
	}
	hookBefore, hadHook := hookJobCreatedAt(t, s.namespace, s.release)

	t.Log("restarting the agent after the install completed")
	restartAgent(t)
	waitForPlugin(t)

	// Re-apply the identical forma, which is what a reconcile does after a
	// restart.
	outcome := waitApply(t, submitApply(t, forma), installTimeout)
	if outcome.State != "Success" {
		t.Fatalf("re-apply ended %s: %s", outcome.State, outcome.Message)
	}

	rel := currentRelease(t, s.namespace, s.release)
	if rel.Revision != 1 {
		t.Errorf("revision = %d after re-applying an unchanged forma, want 1", rel.Revision)
	}
	if hadHook {
		if after, stillThere := hookJobCreatedAt(t, s.namespace, s.release); stillThere && after.After(hookBefore) {
			t.Errorf("pre-install hook re-ran: created %s, was %s", after, hookBefore)
		}
	}
}

// ---------------------------------------------------------------------------
// Row 3: the plugin is stopped gracefully
// ---------------------------------------------------------------------------

// The drain's whole purpose. Cancelling in-flight operations makes Helm run
// failRelease, which records `failed` — a state the next apply upgrades over.
// Being killed without cancelling leaves `pending-install`, which Helm refuses
// to install OR upgrade and which only `helm uninstall` clears.
func TestPluginSigtermMidInstall(t *testing.T) {
	s := newScenario(t, "plugin-term")
	forma := s.writeForma("forma.pkl", "hello", hookSeconds, 600)

	commandID := submitApply(t, forma)
	waitForReleaseStatus(t, s.namespace, s.release, "pending-install", 2*time.Minute)

	t.Log("SIGTERM to the plugin mid-install")
	signalPlugin(t, "TERM")

	rel := awaitSettledRecord(t, s, 60*time.Second)
	if rel.Status == "pending-install" {
		t.Fatal("release left pending-install after a graceful stop; " +
			"recovery needs `helm uninstall`, which is the outcome the drain exists to avoid")
	}
	if rel.Status != "failed" {
		t.Fatalf("release status = %q, want failed", rel.Status)
	}
	t.Log("release landed failed, so the next apply can upgrade over it")

	// The interrupted command has to reach a terminal state before anything can
	// be applied over it — formae refuses a second command against resources one
	// is already working on. It must also not claim success for an install that
	// was cancelled halfway.
	waitForPlugin(t)
	if outcome := waitCommand(t, commandID, installTimeout); outcome.State == "Success" {
		t.Fatal("command reported Success for an install that was cancelled mid-flight")
	}

	// The benefit, demonstrated rather than asserted in the abstract: recovery
	// needs no operator.
	recovery := s.writeForma("recovery.pkl", "hello", 0, 600)
	if outcome := waitApply(t, submitApply(t, recovery), installTimeout); outcome.State != "Success" {
		t.Fatalf("re-apply after a drained stop ended %s: %s", outcome.State, outcome.Message)
	}
	if rel := currentRelease(t, s.namespace, s.release); rel.Status != "deployed" {
		t.Errorf("status after recovery = %q, want deployed", rel.Status)
	}
}

// ---------------------------------------------------------------------------
// Row 4: the plugin is killed outright
// ---------------------------------------------------------------------------

// SIGKILL cannot be drained, so this is the case with no shutdown-time help at
// all — and it is the ordinary one, because `formae agent stop` reaches the
// plugin as SIGKILL too (agent.go SIGTERMs the agent; the supervisor's port then
// calls cmd.Process.Kill()).
//
// So recovery has to happen on the next operation instead. Once the stall window
// proves nothing is coming back, the plugin rewrites the abandoned record — to
// `deployed` if every object it renders is present and ready, otherwise to
// `failed`, which an upgrade is allowed to run over. Either way the command
// converges with no operator and no `helm uninstall`.
//
// The window is twice the release's own timeoutSeconds, which is why this
// scenario sets it to 20s: at the 600s default the same assertion would take
// twenty minutes.
func TestPluginSigkillMidInstall(t *testing.T) {
	s := newScenario(t, "plugin-kill")
	const shortTimeout = 20
	forma := s.writeForma("forma.pkl", "hello", hookSeconds, shortTimeout)

	commandID := submitApply(t, forma)
	waitForReleaseStatus(t, s.namespace, s.release, "pending-install", 2*time.Minute)

	t.Log("SIGKILL to the plugin mid-install")
	signalPlugin(t, "KILL")
	waitForPlugin(t)

	// Immediately after the kill nothing has healed it yet: the lock is held by
	// a process that no longer exists.
	if rel := currentRelease(t, s.namespace, s.release); rel.Status != "pending-install" {
		t.Errorf("status right after SIGKILL = %q, want pending-install", rel.Status)
	}

	// The command fails, and fails quickly rather than hanging. It cannot do
	// anything better: the PluginOperator lived on the plugin's node and died
	// with it, so the agent's ResourceUpdater notices the silence
	// (PluginOperatorMissingInAction) and marks the update failed. Status is
	// never called again, which means the plugin never gets the chance to
	// recover the record on this command.
	outcome := waitCommand(t, commandID, 6*time.Minute)
	t.Logf("command ended %s: %q", outcome.State, outcome.Message)
	if outcome.State == "Success" {
		t.Fatal("command reported Success for an install nothing finished")
	}

	// The documented consequence, asserted so it stays a known cost: the release
	// is left holding Helm's lock until something applies again. Nothing in the
	// plugin can clear it here — the plugin is dead and will not be asked.
	wedged := currentRelease(t, s.namespace, s.release)
	t.Logf("immediately after the kill the release is %q at revision %d", wedged.Status, wedged.Revision)

	// And the guarantee that makes that survivable: the very next apply clears
	// the lock and completes the install, with no operator and no
	// `helm uninstall`. This is the whole contract for a plugin crash.
	waitForPlugin(t)
	recovery := s.writeForma("recovery.pkl", "hello", 0, 600)
	if again := waitApply(t, submitApply(t, recovery), installTimeout); again.State != "Success" {
		t.Fatalf("re-apply after a SIGKILL ended %s: %s — a plugin crash must not need manual recovery",
			again.State, again.Message)
	}

	final := currentRelease(t, s.namespace, s.release)
	if final.Status != "deployed" {
		t.Errorf("status = %q after re-apply, want deployed", final.Status)
	}
	t.Logf("recovered to %q at revision %d without an operator", final.Status, final.Revision)
}

// ---------------------------------------------------------------------------
// How reliable is the drain, really
// ---------------------------------------------------------------------------

// The drain races the SDK's own signal handler: Go delivers to every
// signal.Notify receiver concurrently and offers no ordering hook, so the
// release-record write is in a footrace with node teardown. Winning is the
// common case, but "it worked when I tried it" is not a number.
//
// Losing is not a failure — the outcome then is exactly what it was before the
// drain existed — so this reports a rate and only fails if it collapses.
//
// Off by default: STABILITY_DRAIN_SAMPLES=10 make helm-stability-test
func TestDrainWinRate(t *testing.T) {
	samples := 0
	if raw := os.Getenv("STABILITY_DRAIN_SAMPLES"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("STABILITY_DRAIN_SAMPLES=%q is not a number", raw)
		}
		samples = parsed
	}
	if samples <= 0 {
		t.Skip("set STABILITY_DRAIN_SAMPLES=10 to measure how often the drain wins its race")
	}

	// A floor, not a target. Below this the drain is not buying what it claims
	// to buy and the SDK pre-stop hook stops being a nice-to-have.
	const floor = 0.5

	won, lost, inconclusive := 0, 0, 0
	for i := 0; i < samples; i++ {
		status := drainOnce(t, i)
		switch status {
		case "failed":
			won++
		case "pending-install":
			lost++
		default:
			inconclusive++
		}
		t.Logf("sample %d/%d: %s (won %d, lost %d, inconclusive %d)",
			i+1, samples, status, won, lost, inconclusive)
	}

	decided := won + lost
	if decided == 0 {
		t.Fatalf("every one of %d samples was inconclusive", samples)
	}
	rate := float64(won) / float64(decided)
	t.Logf("drain win rate: %d/%d = %.0f%% (%d inconclusive)", won, decided, rate*100, inconclusive)

	if rate < floor {
		t.Errorf("drain won %.0f%% of races, below the %.0f%% floor; "+
			"a pre-stop hook on the SDK's RunConfig would remove the race entirely",
			rate*100, floor*100)
	}
}

// drainOnce runs one SIGTERM-mid-install cycle and reports the release status it
// left behind.
func drainOnce(t *testing.T, i int) string {
	t.Helper()
	s := newScenario(t, fmt.Sprintf("drain-%d", i))
	forma := s.writeForma("forma.pkl", "hello", hookSeconds, 600)

	commandID := submitApply(t, forma)
	waitForReleaseStatus(t, s.namespace, s.release, "pending-install", 2*time.Minute)

	signalPlugin(t, "TERM")
	rel := awaitSettledRecord(t, s, 60*time.Second)
	waitForPlugin(t)

	// Let the interrupted command finish before the next sample starts. Left
	// InProgress it would block the following apply, which shares this suite's
	// stack, and every later sample would fail for a reason unrelated to the
	// drain.
	waitCommand(t, commandID, installTimeout)

	if !rel.Found {
		return "no-record"
	}
	return rel.Status
}

// awaitSettledRecord waits for the release to stop being pending, or gives up
// and returns whatever it last saw.
//
// Returning rather than failing on timeout is deliberate: still-pending IS the
// answer for a lost race, and the caller decides whether that is a failure.
func awaitSettledRecord(t *testing.T, s *scenario, timeout time.Duration) releaseState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last releaseState
	for time.Now().Before(deadline) {
		last = currentRelease(t, s.namespace, s.release)
		if last.Found && last.Status != "pending-install" {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last
}
