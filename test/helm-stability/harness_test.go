//go:build stability

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Harness for the helm-stability suite: agent and plugin process control, a
// formae CLI wrapper, and an independent view of Helm's release record.
//
// The suite exists because the in-package integration tests can only simulate an
// interrupted operation — they call Create twice in one process. What actually
// has to work is the real thing: a real agent re-driving a real command against
// a real plugin process, and a real signal reaching that plugin. None of the
// three is exercised by calling a Go function twice.
package helmstability

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	targetLabel  = "stability-target"
	stackPrefix  = "helm-stability-"
	resourceType = "K8S::Helm::Release"

	// Namespaces this suite creates. Distinct from every other suite's prefix,
	// because a crash test leaves deliberate wreckage behind and must never be
	// cleaning up after — or be cleaned up by — anything else.
	namespacePrefix = "formae-helm-stability-"
)

var (
	formaeBinary string
	// agentProc is the agent this suite started. Recorded rather than looked up,
	// because `formae agent start` carries no distinguishing arguments — the
	// profile is global CLI state, not a flag — so there is nothing in a process
	// listing that separates our agent from anyone else's. Since this file's job
	// is to send signals, "the only agent running" is not good enough: we kill
	// the process we started, or nothing.
	agentProc *os.Process
	// The formae CLI prints the submitted command's KSUID in the async notice, and
	// the wording of that notice is not a contract. It used to read `id:<ksuid>`;
	// since the CLI split `command status` from `command list` it reads
	// `formae command status <ksuid>`. Match either, so this suite does not go red
	// on a copy edit — every test here begins by submitting an apply, so a missed
	// KSUID fails the whole file before any Helm behaviour is exercised.
	commandIDRegex = regexp.MustCompile(`(?:id:|formae command status\s+)([A-Za-z0-9]+)`)
)

// ---------------------------------------------------------------------------
// Suite setup
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	formaeBinary = envOr("FORMAE_BINARY", "formae")

	if _, err := exec.LookPath(formaeBinary); err != nil {
		fmt.Fprintf(os.Stderr, "skip: formae binary %q not found\n", formaeBinary)
		os.Exit(0)
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		fmt.Fprintln(os.Stderr, "skip: kubectl not found")
		os.Exit(0)
	}
	if out, err := run("kubectl", "version", "--request-timeout=5s"); err != nil {
		fmt.Fprintf(os.Stderr, "skip: no reachable cluster: %v\n%s\n", err, out)
		os.Exit(0)
	}

	// The suite owns the agent for the whole run. In TestMain rather than in a
	// test, because several scenarios restart it and none of them may inherit
	// whatever state a previous run left: an agent already up could be mid-way
	// through re-driving the last run's commands.
	if err := ensureAgentDown(); err != nil {
		fmt.Fprintf(os.Stderr, "could not stop a pre-existing agent: %v\n", err)
		os.Exit(1)
	}
	if err := launchAgent(); err != nil {
		fmt.Fprintf(os.Stderr, "could not start agent: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := ensureAgentDown(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: agent did not stop cleanly: %v\n", err)
	}
	// Leave no plugin behind. An orphan holds its Ergo node name and blocks the
	// next agent from ever starting that plugin, so a run that leaks one breaks
	// whatever runs next — including work that has nothing to do with this suite.
	if reaped := reapOrphanedProcesses(); reaped > 0 {
		fmt.Fprintf(os.Stderr, "reaped %d orphaned plugin process(es) on exit\n", reaped)
	}
	os.Exit(code)
}

// reapOrphanedProcesses is reapOrphanedPlugins without a *testing.T, for
// TestMain. Same rule: orphans only, never a plugin whose agent is alive.
func reapOrphanedProcesses() int {
	out, err := run("pgrep", "-f", "/formae/plugins/")
	if err != nil {
		return 0
	}
	reaped := 0
	for _, field := range strings.Fields(out) {
		ppid, ppidErr := run("ps", "-o", "ppid=", "-p", field)
		if ppidErr != nil || strings.TrimSpace(ppid) != "1" {
			continue
		}
		if _, killErr := run("kill", "-9", field); killErr == nil {
			reaped++
		}
	}
	return reaped
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Agent lifecycle
// ---------------------------------------------------------------------------

// launchAgent brings the suite's own agent up and waits for it to answer.
//
// The profile is whatever `formae profile use` last selected; the runner script
// owns that and restores it afterwards. There is no per-invocation profile flag
// in this CLI, which is also why the suite cannot run alongside other formae
// work — see the README.
func launchAgent() error {
	// `agent start` runs in the foreground until stopped, so it is detached
	// here and reaped through the CLI.
	cmd := exec.Command(formaeBinary, "agent", "start")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	agentProc = cmd.Process
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if agentUp() {
			return nil
		}
		if !processAlive(agentProc.Pid) {
			return fmt.Errorf("agent process %d exited during startup", agentProc.Pid)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("did not answer within 60s")
}

// ensureAgentDown stops the agent and waits for it to actually be gone. Safe
// when none is running.
func ensureAgentDown() error {
	_, _ = run(formaeBinary, "agent", "stop")

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if !agentUp() {
			agentProc = nil
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("still answering after 60s")
}

func processAlive(pid int) bool {
	_, err := run("ps", "-p", strconv.Itoa(pid), "-o", "pid=")
	return err == nil
}

// restartAgent stops the agent gracefully and brings it back.
//
// Measured behaviour, and it is not what you would assume: a graceful stop takes
// the plugin down with it, so the in-flight Helm operation dies too. Use
// crashAgent for the case where the install has to survive.
func restartAgent(t *testing.T) {
	t.Helper()
	if err := ensureAgentDown(); err != nil {
		t.Fatalf("stop agent: %v", err)
	}
	if err := launchAgent(); err != nil {
		t.Fatalf("restart agent: %v", err)
	}
}

// crashAgent SIGKILLs the agent and reports what became of its plugin.
//
// Whether the plugin outlives the agent is a platform property, and the suite
// discovers it rather than assuming it. formae asks the kernel to clean up:
//
//	procattr_linux.go  -> SysProcAttr{Pdeathsig: syscall.SIGKILL}
//	procattr_other.go  -> nil ("cleaned up via the supervisor's explicit
//	                      termination on shutdown")
//
// So on Linux the kernel kills the plugin with its parent and the install dies;
// on macOS the plugin is orphaned onto pid 1 and keeps installing. Both are
// legitimate, and the recovery has to work either way — which is why the test
// branches on pluginSurvived rather than on runtime.GOOS. Reading the outcome
// off the machine also means the test keeps telling the truth if the platform
// behaviour changes, instead of asserting what it used to be.
//
// Either way a restarted agent does NOT adopt a surviving plugin; it spawns a
// fresh one and the orphan lingers, so callers should reap it.
func crashAgent(t *testing.T) (orphan int, pluginSurvived bool) {
	t.Helper()
	orphan, hadPlugin := pluginPID(t)

	pid := agentPID(t)
	if _, err := run("kill", "-9", strconv.Itoa(pid)); err != nil {
		t.Fatalf("kill -9 agent %d: %v", pid, err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			agentProc = nil
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if agentProc != nil {
		t.Fatalf("agent %d survived SIGKILL", pid)
	}
	if !hadPlugin {
		return 0, false
	}

	// Give Pdeathsig a moment to land before concluding the plugin outlived its
	// parent. It fires on the parent thread exiting, not instantly.
	settle := time.Now().Add(5 * time.Second)
	for time.Now().Before(settle) {
		if !processAlive(orphan) {
			return orphan, false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return orphan, processAlive(orphan)
}

// reapOrphanedPlugins kills every plugin process left parentless, and reports
// how many it found.
//
// Not tidiness — required for the next agent to work at all. A plugin registers
// an Ergo node name, and an orphan keeps holding it, so the replacement a new
// agent spawns dies with "unable to register node: resource is taken". The
// supervisor retries MaxPluginRestarts times and gives up, leaving that
// namespace with no plugin and every command against it failing with
// "plugin operator is missing in action". One survivor wedges every later run.
//
// This is precisely the cleanup Linux gets from Pdeathsig and macOS does not
// (procattr_linux.go vs procattr_other.go), which is why calling it explicitly
// is what makes the crash scenarios behave the same on both.
//
// Scoped to orphans: a plugin whose parent is still alive belongs to a running
// agent — possibly somebody else's — and is left alone. Ours are reached through
// the agent that owns them.
func reapOrphanedPlugins(t *testing.T) int {
	t.Helper()

	out, err := run("pgrep", "-f", "/formae/plugins/")
	if err != nil {
		return 0
	}

	reaped := 0
	for _, field := range strings.Fields(out) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		ppid, ppidErr := run("ps", "-o", "ppid=", "-p", field)
		if ppidErr != nil || strings.TrimSpace(ppid) != "1" {
			continue
		}
		if _, killErr := run("kill", "-9", field); killErr == nil {
			t.Logf("reaped orphaned plugin %d", pid)
			reaped++
		}
	}
	return reaped
}

func agentUp() bool {
	_, err := run(formaeBinary, "status", "agent")
	return err == nil
}

// ---------------------------------------------------------------------------
// Plugin process control
// ---------------------------------------------------------------------------

// agentPID returns the pid of the agent this suite started.
//
// Never a process listing lookup. `formae agent start` takes no arguments that
// distinguish one agent from another — the profile is global CLI state rather
// than a flag — so `pgrep -f "formae.*agent start"` matches every agent on the
// machine equally. That is fine for reading and fatal for signalling, and this
// pid is the parent of the plugin the suite is about to kill.
func agentPID(t *testing.T) int {
	t.Helper()
	if agentProc == nil {
		t.Fatal("no agent started by this suite")
	}
	if !processAlive(agentProc.Pid) {
		t.Fatalf("agent %d started by this suite is no longer running", agentProc.Pid)
	}
	return agentProc.Pid
}

// pluginPID finds the k8s plugin process belonging to this suite's agent.
//
// Resolved as a CHILD of our own agent pid, never by name. Several agents can
// run at once — a developer's, CI's, another suite's — and they all spawn a
// process from the same `~/.pel/formae/plugins/k8s/<version>/k8s` path, so
// `pgrep -f k8s` would return every one of them.
func pluginPID(t *testing.T) (int, bool) {
	t.Helper()
	out, err := run("pgrep", "-P", strconv.Itoa(agentPID(t)))
	if err != nil {
		return 0, false
	}
	for _, field := range strings.Fields(out) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		cmdline, cmdErr := run("ps", "-p", field, "-o", "command=")
		if cmdErr != nil {
			continue
		}
		if strings.Contains(cmdline, "/plugins/k8s/") {
			return pid, true
		}
	}
	return 0, false
}

// signalPlugin delivers sig to the plugin and waits for the process to go.
//
// SIGTERM is the graceful stop the drain handles; SIGKILL is the floor it
// cannot. The difference between the two is the entire subject of this suite.
func signalPlugin(t *testing.T, sig string) {
	t.Helper()
	pid, ok := pluginPID(t)
	if !ok {
		t.Fatal("no k8s plugin process under this suite's agent")
	}
	if _, err := run("kill", "-"+sig, strconv.Itoa(pid)); err != nil {
		t.Fatalf("kill -%s %d: %v", sig, pid, err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := run("ps", "-p", strconv.Itoa(pid), "-o", "pid="); err != nil {
			t.Logf("plugin pid %d gone after SIG%s", pid, sig)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("plugin pid %d survived SIG%s for 60s", pid, sig)
}

// waitForPlugin blocks until the agent has respawned a plugin process, so a
// following apply is not racing the respawn.
func waitForPlugin(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := pluginPID(t); ok {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("agent did not respawn the k8s plugin within 90s")
}

// ---------------------------------------------------------------------------
// formae CLI
// ---------------------------------------------------------------------------

// submitApply submits a command and returns its id without waiting for it.
//
// Not waiting is the point: every scenario here acts while the command is still
// in flight.
func submitApply(t *testing.T, forma string) string {
	t.Helper()
	out, _ := runCombined(formaeBinary,
		"apply", "--mode", "reconcile", "--yes", forma)

	// An apply whose forma already matches reality submits no command at all and
	// says so. That is the strongest possible pass for a re-apply scenario — it
	// means formae saw nothing to change — so it is a result, not a failure.
	if strings.Contains(out, "No changes needed") {
		return ""
	}

	match := commandIDRegex.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no command submitted: %s", strings.TrimSpace(out))
	}
	return match[1]
}

// waitApply waits for a submitted command, treating "nothing to do" as success.
func waitApply(t *testing.T, commandID string, timeout time.Duration) commandOutcome {
	t.Helper()
	if commandID == "" {
		return commandOutcome{State: "Success", Message: "no changes needed"}
	}
	return waitCommand(t, commandID, timeout)
}

type commandOutcome struct {
	State   string
	Message string
}

// waitCommand polls until the command reaches a terminal state.
func waitCommand(t *testing.T, id string, timeout time.Duration) commandOutcome {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "unknown"

	for time.Now().Before(deadline) {
		raw, err := run(formaeBinary, "status", "command",
			"--query=id:"+id, "--output-consumer", "machine")
		if err != nil {
			// Expected while the agent is down mid-restart.
			time.Sleep(2 * time.Second)
			continue
		}

		var payload struct {
			Commands []struct {
				State           string
				ErrorMessage    string
				ResourceUpdates []struct {
					ResourceType string
					ErrorMessage string
					State        string
				}
			}
		}
		if json.Unmarshal([]byte(raw), &payload) != nil || len(payload.Commands) == 0 {
			time.Sleep(2 * time.Second)
			continue
		}

		cmd := payload.Commands[0]
		last = cmd.State
		switch cmd.State {
		case "Success", "Failed", "Rejected", "Canceled":
			message := cmd.ErrorMessage
			for _, update := range cmd.ResourceUpdates {
				if strings.HasSuffix(update.ResourceType, "Release") && update.ErrorMessage != "" {
					message = update.ErrorMessage
				}
			}
			return commandOutcome{State: cmd.State, Message: message}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("command %s did not settle within %s (last state %q)", id, timeout, last)
	return commandOutcome{}
}

// ---------------------------------------------------------------------------
// Helm's release record, read independently
// ---------------------------------------------------------------------------

// releaseState is what Helm itself says about the release.
//
// Read from the release Secret's labels with kubectl rather than through the
// plugin or the helm CLI. Through the plugin the oracle would share code with
// the thing under test; through the helm CLI it would depend on the CLI's major
// version matching the SDK the plugin embeds. The labels are Helm's own storage
// format and neither problem applies.
type releaseState struct {
	Status   string
	Revision int
	Found    bool
}

func currentRelease(t *testing.T, namespace, name string) releaseState {
	t.Helper()
	out, err := run("kubectl", "get", "secret", "-n", namespace,
		"-l", "owner=helm,name="+name,
		"-o", `jsonpath={range .items[*]}{.metadata.labels.version} {.metadata.labels.status}{"\n"}{end}`)
	if err != nil || strings.TrimSpace(out) == "" {
		return releaseState{}
	}

	// Helm keeps one Secret per revision; the highest is the current one.
	state := releaseState{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		revision, convErr := strconv.Atoi(fields[0])
		if convErr != nil {
			continue
		}
		if !state.Found || revision > state.Revision {
			state = releaseState{Status: fields[1], Revision: revision, Found: true}
		}
	}
	return state
}

func waitForReleaseStatus(t *testing.T, namespace, name, want string, timeout time.Duration) releaseState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last releaseState
	for time.Now().Before(deadline) {
		last = currentRelease(t, namespace, name)
		if last.Found && last.Status == want {
			return last
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("release %s/%s did not reach %q within %s (last %+v)", namespace, name, want, timeout, last)
	return last
}

// hookJobCreatedAt reports when the chart's pre-install hook Job was created, so
// a test can tell a hook that re-ran from one that did not.
func hookJobCreatedAt(t *testing.T, namespace, name string) (time.Time, bool) {
	t.Helper()
	out, err := run("kubectl", "get", "job", name+"-preinstall", "-n", namespace,
		"-o", "jsonpath={.metadata.creationTimestamp}")
	if err != nil || strings.TrimSpace(out) == "" {
		return time.Time{}, false
	}
	stamp, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if parseErr != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// ---------------------------------------------------------------------------
// Forma fixtures
// ---------------------------------------------------------------------------

// scenario is one release under test, with its own namespace and working
// directory so scenarios cannot interfere with one another.
type scenario struct {
	t         *testing.T
	work      string
	namespace string
	release   string
	// stack is per-scenario, not shared. formae refuses a command against
	// resources another command is already working on, and every scenario here
	// ends with a command that was interrupted on purpose — so a shared stack
	// means the first scenario's wreckage blocks the apply of every scenario
	// after it, each failing for a reason that has nothing to do with what it
	// was testing.
	stack string
}

func newScenario(t *testing.T, name string) *scenario {
	t.Helper()
	s := &scenario{
		t:         t,
		work:      t.TempDir(),
		namespace: namespacePrefix + name,
		release:   name,
		stack:     stackPrefix + name,
	}
	s.writePklProject()
	t.Cleanup(s.teardown)

	// A fresh agent per scenario, because the supervisor gives up on a plugin
	// after MaxPluginRestarts (5, plugin_process_supervisor.go:29) and every
	// scenario here kills one. Without this the sixth kill in a run leaves the
	// plugin permanently down and every later scenario fails for that reason
	// rather than its own — which is exactly how the first full-suite run
	// reported four failures with one cause.
	restartAgent(t)
	waitForPlugin(t)
	return s
}

// writeForma renders a forma for this scenario and returns its path.
//
// hookSeconds sets how long the chart's pre-install hook takes, which is what
// gives every scenario a reliable window to act in. timeoutSeconds is passed
// through so the SIGKILL case can shrink the stall window to something a test
// can wait out.
func (s *scenario) writeForma(fileName, message string, hookSeconds, timeoutSeconds int) string {
	s.t.Helper()

	chart, err := filepath.Abs(filepath.Join("..", "..", "testdata", "charts", "hooked"))
	if err != nil {
		s.t.Fatal(err)
	}

	content := fmt.Sprintf(`amends "@formae/forma.pkl"

import "@formae/formae.pkl"
import "@k8s/v%s/k8s.pkl" as k8s
import "@k8s/helm/Release.pkl" as helm

forma {
  new formae.Stack {
    label = "%s"
    description = "helm-stability suite"
  }

  new formae.Target {
    label = "%s"
    config = new k8s.Config {
      kubernetesVersion = "%s"
      auth = new k8s.KubeconfigAuth {}
    }
  }

  new helm.Release {
    label = "%s"
    metadata {
      name = "%s"
      namespace = "%s"
    }
    chart = "%s"
    createNamespace = true
    timeoutSeconds = %d
    values = new Dynamic {
      message = "%s"
      hookSleepSeconds = %d
    }
  }
}
`, kubeVersion(), s.stack, targetLabel, kubeVersion(),
		s.release, s.release, s.namespace,
		chart, timeoutSeconds, message, hookSeconds)

	path := filepath.Join(s.work, fileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		s.t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func (s *scenario) writePklProject() {
	s.t.Helper()
	schema, err := filepath.Abs(filepath.Join("..", "..", "schema", "pkl"))
	if err != nil {
		s.t.Fatal(err)
	}

	content := fmt.Sprintf(`amends "pkl:Project"

dependencies {
  ["formae"] {
    uri = "package://hub.platform.engineering/plugins/pkl/schema/pkl/formae/formae@%s"
  }
  ["k8s"] = import("%s/PklProject")
}
`, formaePklPackage(), schema)

	if err := os.WriteFile(filepath.Join(s.work, "PklProject"), []byte(content), 0o600); err != nil {
		s.t.Fatalf("write PklProject: %v", err)
	}
	if out, err := run("pkl", "project", "resolve", s.work); err != nil {
		s.t.Fatalf("pkl project resolve: %v\n%s", err, out)
	}
}

// teardown removes the namespace outright rather than asking formae to destroy
// the release.
//
// Deliberate: a scenario ends with the release wedged on purpose about half the
// time, and `formae destroy` against a pending-install release is the very thing
// these tests prove does not work. Deleting the namespace is the recovery an
// operator would reach for, and it cannot be blocked by Helm's lock.
// assertNotStuck is the guarantee the whole suite exists to defend: however the
// agent or the plugin died, the release must not be left holding a Helm lock.
//
// A release in any pending-* state is stuck by definition. Helm refuses both
// install and upgrade on one, so every later apply against it fails, and the
// only documented way out is `helm uninstall` — which destroys the objects and
// re-runs pre-install hooks. Ending a scenario in that state means a crash has
// cost someone manual recovery, which is exactly what must not happen.
//
// Run automatically at the end of every scenario, so a test cannot pass by
// asserting its own narrow thing while leaving the release wedged.
func (s *scenario) assertNotStuck() {
	rel := currentRelease(s.t, s.namespace, s.release)
	if !rel.Found {
		// Nothing left behind at all: uninstalled, or never created. Not stuck.
		return
	}
	if strings.HasPrefix(rel.Status, "pending-") || rel.Status == "uninstalling" {
		s.t.Errorf("release %s/%s left stuck in %q at revision %d — "+
			"Helm refuses install and upgrade in this state, so recovery needs `helm uninstall`",
			s.namespace, s.release, rel.Status, rel.Revision)
		return
	}
	s.t.Logf("release settled at %q revision %d — not stuck", rel.Status, rel.Revision)
}

// assertNoLeftovers reports objects the scenario left behind that no release
// accounts for.
//
// A crash mid-install leaves whatever Helm had managed to create, and recovery
// then either adopts it (recorded `deployed`) or upgrades over it (three-way
// merged). Either way the namespace should end up holding exactly what the chart
// renders — plus Helm's own release Secrets and whatever Kubernetes puts in
// every namespace.
//
// Hook residue is reported, not failed, because it is Helm's own behaviour and
// not something this plugin could fix without overriding it. In hooks.go,
// deletion happens only inside execHook and only on an outcome:
//
//	 61  deleteHookByPolicy(h, HookBeforeHookCreation)  before creating
//	103  deleteHookByPolicy(h, HookFailed)              on failure
//	127  deleteHookByPolicy(h, HookSucceeded)           on success
//
// Kill the process mid-hook and execHook reaches none of them, so the Job stays.
// An interrupted `helm install` from the CLI leaves exactly the same thing, and
// Helm's design assumes it: line 58 defaults every hook to
// `before-hook-creation`, i.e. stale hooks are cleaned when that hook is next
// created rather than reaped eagerly.
//
// The catch worth knowing is that "next created" means the same hook. Recovery
// here is an upgrade, which runs `pre-upgrade` hooks — so a `pre-install`-only
// hook like this chart's is never re-created and its Job persists until someone
// installs the release fresh. A chart declaring `pre-install,pre-upgrade` (as
// kratos does for its migration) is cleaned by the recovery upgrade itself.
func (s *scenario) assertNoLeftovers() {
	for _, kind := range []string{"job", "pod"} {
		out, err := run("kubectl", "get", kind, "-n", s.namespace,
			"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
		if err != nil {
			continue
		}
		for _, name := range strings.Fields(out) {
			if strings.Contains(name, "-preinstall") {
				s.t.Logf("hook %s %q survived in %s — expected: Helm reaps hooks on "+
					"outcome, and an interrupted hook has none. Cleaned when that hook "+
					"is next created, which for a pre-install-only hook is a fresh install",
					kind, name, s.namespace)
			}
		}
	}

	// Everything else, logged. Anything unexpected here is a lead, not a verdict.
	out, err := run("kubectl", "get", "configmap,secret,deployment,statefulset,service,serviceaccount",
		"-n", s.namespace, "--no-headers",
		"-o", "custom-columns=KIND:.kind,NAME:.metadata.name")
	if err == nil && strings.TrimSpace(out) != "" {
		s.t.Logf("objects remaining in %s:\n%s", s.namespace, strings.TrimSpace(out))
	}
}

func (s *scenario) teardown() {
	s.assertNotStuck()
	s.assertNoLeftovers()

	if os.Getenv("STABILITY_KEEP") != "" {
		s.t.Logf("STABILITY_KEEP set; leaving namespace %s", s.namespace)
		return
	}
	_, _ = run("kubectl", "delete", "namespace", s.namespace, "--wait=false", "--ignore-not-found")
	_, _ = run(formaeBinary, "destroy", "--query",
		"stack:"+s.stack, "--yes")
}

func kubeVersion() string { return envOr("STABILITY_KUBE_VERSION", "1.33") }

func formaePklPackage() string { return envOr("STABILITY_FORMAE_PKL", "0.88.1") }

// ---------------------------------------------------------------------------
// Process helpers
// ---------------------------------------------------------------------------

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func runCombined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
