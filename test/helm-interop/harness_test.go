//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Support for the formae<->Helm interop test: chart specs, the two Helm drivers,
// and the formae CLI wrapper. The scenario itself is in interop_test.go.
//
// This package deliberately depends on nothing from the plugin it exercises. It
// talks to Helm through the SDK the way any client would, and re-declares the
// contracts it asserts on — the ownership label, the resource type, the native
// id format — as literals. Importing those from the plugin would mean a test
// that follows a change to them instead of catching it, which for a wire-format
// contract is precisely backwards.
package interop

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"sigs.k8s.io/yaml"
)

// Contracts this suite asserts against, restated rather than imported.
const (
	// Stamped by the plugin as a Helm release label on every release it
	// installs or upgrades. Its absence is what makes a foreign release an
	// adoption candidate rather than something to overwrite.
	formaeManagedLabel = "formae.dev/managed"

	// Resource type as formae names it.
	resourceTypeRelease = "K8S::Helm::Release"

	// Helm's storage driver. Releases live in Secrets named
	// sh.helm.release.v1.<name>.v<revision>.
	helmStorageDriver = "secret"
)

// nativeID is how formae identifies a release: "<namespace>/<name>".
func nativeID(namespace, release string) string {
	return namespace + "/" + release
}

// ---------------------------------------------------------------------------
// Chart specs — a directory of files, one per chart
// ---------------------------------------------------------------------------

// specDir holds one file per chart under test. Adding a chart is adding a file;
// there is no registry to also remember to edit.
//
// A bare `<name>.yaml` describes the release as `helm install` should first
// create it, and gets the install/discover/adopt chain. A sibling
// `<name>-migrate.yaml` names the version to move to, and its presence is what
// opts that chart into the rest: formae upgrade, helm rollback, reconcile. A
// chart with nothing to migrate to simply does not get those steps rather than
// getting them and skipping.
// Chart specs sit beside the suite that runs them.
var specDir = "charts"

const migrateSuffix = "-migrate"

// chartSpec is `<name>.yaml`: the release as it starts life.
type chartSpec struct {
	// Chart reference as Helm takes it — "repo/chart" or an oci:// URL.
	Chart string `json:"chart"`
	// Repository the chart comes from. Not needed to install from a repo already
	// added, and never recoverable from a release record, which is why formae
	// requires it the moment a version changes.
	RepoURL string `json:"repoURL"`
	// Version `helm install` pins. The starting point, not the target.
	Version string `json:"version"`
	// Values handed to the install, verbatim.
	Values map[string]any `json:"values"`

	// Why this chart is in the set, and the object that proves it. A cell whose
	// assertions never touch its trait is padding — see assertTrait.
	Trait string `json:"trait"`
	// "Kind/name" of the object the trait assertion looks for.
	TraitObject string `json:"traitObject"`

	// Notes carries anything a reader needs that the fields do not say. Ignored
	// by the harness.
	Notes string `json:"notes"`

	name string // from the filename
}

// migrateSpec is `<name>-migrate.yaml`: where formae should move the release to.
// Values are optional; when empty the install's values carry over, so the cell
// measures a version change and nothing else.
type migrateSpec struct {
	Version string         `json:"version"`
	Values  map[string]any `json:"values"`
	Notes   string         `json:"notes"`
}

// loadSpecs reads every chart spec, pairing each with its migrate file if there
// is one.
func loadSpecs(t *testing.T) []specPair {
	t.Helper()

	entries, err := os.ReadDir(specDir)
	if err != nil {
		t.Fatalf("read %s: %v", specDir, err)
	}

	var pairs []specPair
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		base := strings.TrimSuffix(name, ".yaml")
		if strings.HasSuffix(base, migrateSuffix) {
			continue // picked up alongside its chart, not on its own
		}

		var spec chartSpec
		readYAML(t, filepath.Join(specDir, name), &spec)
		spec.name = base
		if spec.Chart == "" || spec.Version == "" {
			t.Fatalf("%s: chart and version are required", name)
		}

		pair := specPair{chart: spec}
		migratePath := filepath.Join(specDir, base+migrateSuffix+".yaml")
		if _, err := os.Stat(migratePath); err == nil {
			var migrate migrateSpec
			readYAML(t, migratePath, &migrate)
			if migrate.Version == "" {
				t.Fatalf("%s: version is required", filepath.Base(migratePath))
			}
			pair.migrate = &migrate
		}
		pairs = append(pairs, pair)
	}
	if len(pairs) == 0 {
		t.Fatalf("no chart specs in %s", specDir)
	}
	return pairs
}

type specPair struct {
	chart   chartSpec
	migrate *migrateSpec // nil when the chart opts out of the migrate path
}

func (p specPair) migrates() bool { return p.migrate != nil }

func readYAML(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// sigs.k8s.io/yaml converts to JSON first, so the struct tags above are
	// json tags and anything nested lands as the map[string]any Helm wants.
	if err := yaml.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Helm, behind an interface
// ---------------------------------------------------------------------------

// releaseState is the part of a Helm release the scenario asserts on. Narrow on
// purpose: whatever both drivers can answer identically.
type releaseState struct {
	Version  string // chart version, e.g. "12.0.3"
	Revision int
	Status   string
	Labels   map[string]string
	// What Helm says produced this revision: "Upgrade complete",
	// "Rollback to 1", "Install complete". This is the only observable that says
	// which *verb* wrote a revision, and therefore which hook events fired — see
	// assertPreRollback.
	Description string
}

// helmClient is how the test talks to Helm.
//
// The interface exists so the choice of implementation stays reversible. The SDK
// driver is the default because it runs the same code the plugin runs —
// newActionConfig and loadChart are the provisioner's own — so an assertion
// about a release is made against the same primitives rather than against
// another tool's incidental output format. The CLI driver is the independent
// witness: a second implementation cannot catch a bug it shares with the first,
// and "a human ran helm" is the thing the scenario is actually about.
//
// Pick with INTEROP_HELM=cli. Anything else, including unset, gets the SDK.
type helmClient interface {
	Install(spec chartSpec, namespace, release string) error
	Rollback(namespace, release string, revision int) error
	State(namespace, release string) (*releaseState, error)
	History(namespace, release string) ([]releaseState, error)
	Uninstall(namespace, release string) error
	Name() string
}

// interopInstallTimeout caps how long a chart gets to become ready. Sweeping
// many charts at once, a few will never come up on a given cluster, and at ten
// minutes each those dominate the run. INTEROP_TIMEOUT=4m keeps a broad sweep
// finishing; leave it unset for a real single-chart run.
func interopInstallTimeout() time.Duration {
	if raw := os.Getenv("INTEROP_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
	}
	return 10 * time.Minute
}

func newHelmDriver(t *testing.T) helmClient {
	t.Helper()
	if strings.EqualFold(os.Getenv("INTEROP_HELM"), "cli") {
		return &cliHelm{t: t}
	}
	return &sdkHelm{t: t}
}

// --- SDK driver -------------------------------------------------------------

type sdkHelm struct {
	t *testing.T
}

func (h *sdkHelm) Name() string { return "sdk" }

// config builds a Helm action configuration against the ambient kubeconfig —
// the same cluster `helm` on PATH would reach, and the same one the plugin's
// Kubeconfig auth resolves to.
//
// Cheap to construct; the underlying clients are lazy. Built per operation
// rather than cached, because Helm mutates fields on the configuration during
// an action and a cached one would pin a namespace.
func (h *sdkHelm) config(namespace string) (*action.Configuration, error) {
	settings := cli.New()
	settings.SetNamespace(namespace)

	conf := new(action.Configuration)
	if err := conf.Init(settings.RESTClientGetter(), namespace, helmStorageDriver,
		func(string, ...any) {}); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}

	// Required for oci:// chart references; Helm errors on any OCI pull without
	// it, and most charts are distributed that way now.
	rc, err := registry.NewClient(registry.ClientOptEnableCache(true))
	if err != nil {
		return nil, fmt.Errorf("init helm registry client: %w", err)
	}
	conf.RegistryClient = rc
	return conf, nil
}

func (h *sdkHelm) Install(spec chartSpec, namespace, release string) error {
	conf, err := h.config(namespace)
	if err != nil {
		return err
	}

	// Built through NewInstall rather than a bare ChartPathOptions because the
	// constructor copies the registry client into it — without that any oci://
	// reference fails to resolve.
	install := action.NewInstall(conf)
	install.ReleaseName = release
	install.Namespace = namespace
	install.CreateNamespace = true
	install.Version = spec.Version
	install.ChartPathOptions.RepoURL = spec.RepoURL
	install.ChartPathOptions.Version = spec.Version

	settings := cli.New()
	path, err := install.ChartPathOptions.LocateChart(spec.Chart, settings)
	if err != nil {
		return fmt.Errorf("locate chart %q: %w", spec.Chart, err)
	}
	chart, err := loader.Load(path)
	if err != nil {
		return fmt.Errorf("load chart %q: %w", spec.Chart, err)
	}
	install.Wait = true
	install.Timeout = interopInstallTimeout()

	values := spec.Values
	if values == nil {
		values = map[string]any{}
	}
	if _, err := install.Run(chart, values); err != nil {
		return err
	}
	return nil
}

func (h *sdkHelm) Rollback(namespace, release string, revision int) error {
	conf, err := h.config(namespace)
	if err != nil {
		return err
	}
	rollback := action.NewRollback(conf)
	rollback.Version = revision
	rollback.Wait = true
	rollback.Timeout = interopInstallTimeout()
	return rollback.Run(release)
}

func (h *sdkHelm) State(namespace, release string) (*releaseState, error) {
	conf, err := h.config(namespace)
	if err != nil {
		return nil, err
	}
	rel, err := conf.Releases.Last(release)
	if err != nil {
		return nil, err
	}
	return stateOf(rel), nil
}

func (h *sdkHelm) History(namespace, release string) ([]releaseState, error) {
	conf, err := h.config(namespace)
	if err != nil {
		return nil, err
	}
	revisions, err := conf.Releases.History(release)
	if err != nil {
		return nil, err
	}
	out := make([]releaseState, 0, len(revisions))
	for _, rev := range revisions {
		out = append(out, *stateOf(rev))
	}
	return out, nil
}

func (h *sdkHelm) Uninstall(namespace, release string) error {
	conf, err := h.config(namespace)
	if err != nil {
		return err
	}
	_, err = action.NewUninstall(conf).Run(release)
	return err
}

func stateOf(rel *release.Release) *releaseState {
	state := &releaseState{Revision: rel.Version, Labels: rel.Labels}
	if rel.Info != nil {
		state.Status = rel.Info.Status.String()
		state.Description = rel.Info.Description
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		state.Version = rel.Chart.Metadata.Version
	}
	return state
}

// --- CLI driver -------------------------------------------------------------

type cliHelm struct{ t *testing.T }

func (h *cliHelm) Name() string { return "cli" }

func (h *cliHelm) Install(spec chartSpec, namespace, release string) error {
	args := []string{
		"install", release, spec.Chart,
		"--version", spec.Version,
		"--namespace", namespace, "--create-namespace",
		"--wait", "--timeout", interopInstallTimeout().String(),
	}
	if spec.RepoURL != "" {
		args = append(args, "--repo", spec.RepoURL)
	}
	if len(spec.Values) > 0 {
		file, err := os.CreateTemp(h.t.TempDir(), "values-*.yaml")
		if err != nil {
			return err
		}
		raw, err := yaml.Marshal(spec.Values)
		if err != nil {
			return err
		}
		if _, err := file.Write(raw); err != nil {
			return err
		}
		_ = file.Close()
		args = append(args, "-f", file.Name())
	}
	_, err := runCmd("helm", args...)
	return err
}

func (h *cliHelm) Rollback(namespace, release string, revision int) error {
	_, err := runCmd("helm", "rollback", release, fmt.Sprint(revision),
		"-n", namespace, "--wait", "--timeout", interopInstallTimeout().String())
	return err
}

func (h *cliHelm) State(namespace, release string) (*releaseState, error) {
	history, err := h.History(namespace, release)
	if err != nil || len(history) == 0 {
		return nil, err
	}
	return &history[len(history)-1], nil
}

func (h *cliHelm) History(namespace, release string) ([]releaseState, error) {
	out, err := runCmd("helm", "history", release, "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Revision    int    `json:"revision"`
		Status      string `json:"status"`
		Chart       string `json:"chart"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, err
	}
	states := make([]releaseState, 0, len(rows))
	for _, row := range rows {
		states = append(states, releaseState{
			// "<name>-<version>", and chart names contain hyphens while versions
			// do not, so the split is on the last one.
			Version:     row.Chart[strings.LastIndex(row.Chart, "-")+1:],
			Revision:    row.Revision,
			Status:      row.Status,
			Description: row.Description,
		})
	}
	return states, nil
}

func (h *cliHelm) Uninstall(namespace, release string) error {
	_, err := runCmd("helm", "uninstall", release, "-n", namespace)
	return err
}

// ---------------------------------------------------------------------------
// formae, over its CLI
// ---------------------------------------------------------------------------

// formaeCLI drives the half of the scenario that has no in-process equivalent:
// discovery, extract and apply are the agent's, not the plugin's.
type formaeCLI struct {
	t      *testing.T
	binary string
	// profile, when set, is prepended to every invocation. The suite needs an
	// agent with discovery enabled, which run-helm-interop-test.sh starts under
	// its own profile; without routing the CLI to the same one, commands would
	// go to whatever agent happens to be the default and the run would assert
	// against a different datastore entirely.
	profile string
}

func newFormaeCLI(t *testing.T) *formaeCLI {
	t.Helper()
	binary := os.Getenv("FORMAE_BINARY")
	if binary == "" {
		found, err := exec.LookPath("formae")
		if err != nil {
			t.Skipf("formae binary not found; set FORMAE_BINARY")
		}
		binary = found
	}
	return &formaeCLI{t: t, binary: binary, profile: os.Getenv("INTEROP_FORMAE_PROFILE")}
}

// args prefixes the profile flag when one is configured. It goes before the
// subcommand: `formae --profile X apply ...`.
func (f *formaeCLI) args(rest ...string) []string {
	if f.profile == "" {
		return rest
	}
	return append([]string{"--profile", f.profile}, rest...)
}

// The formae CLI prints the submitted command's KSUID in the async notice, and
// the wording of that notice is not a contract. It used to read `id:<ksuid>`;
// since the CLI split `command status` from `command list` it reads
// `formae command status <ksuid>`. Match either, so this suite does not go red
// on a copy edit — every test here begins by submitting an apply, so a missed
// KSUID fails the whole file before any Helm behaviour is exercised.
var commandIDPattern = regexp.MustCompile(`(?:id:|formae command status\s+)([A-Za-z0-9]+)`)

// applyAttempts bounds the retries for a rejected apply. Six with quadratic
// backoff spans about four minutes, which covers a sync of the largest chart in
// the set; four with a flat 10s did not.
const applyAttempts = 6

// firstMeaningfulLine picks the first non-blank line, for CLI output whose
// first line is often empty.
func firstMeaningfulLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return out
}

// Apply submits a forma and waits for it to settle. Returns the first
// release-scoped error message, which is empty on success.
//
// `formae apply` takes the forma positionally and exits 0 even when it rejects
// one, putting the reason on stderr — so both streams are read and the exit code
// is not the signal.
func (f *formaeCLI) Apply(mode, forma string) (state string, message string) {
	f.t.Helper()
	return f.apply(mode, forma, true)
}

// ApplyExpectingRefusal submits an apply that SHOULD be turned down, and does
// not retry.
//
// Apply retries a rejection because it is usually a concurrency conflict rather
// than a verdict. Here it is the verdict — reconcile refusing to overwrite an
// out-of-band rollback is the behaviour under test — and retrying would keep
// resubmitting until something let it through, turning the assertion into a
// race against the drift guard.
func (f *formaeCLI) ApplyExpectingRefusal(mode, forma string) (state string, message string) {
	f.t.Helper()
	return f.apply(mode, forma, false)
}

func (f *formaeCLI) apply(mode, forma string, retry bool) (state string, message string) {
	f.t.Helper()
	for attempt := 1; ; attempt++ {
		state, message = f.applyOnce(mode, forma)
		// Rejected means the submit never reached the plugin. Besides real drift,
		// a background sync landing between the CLI's pre-flight read and its
		// submit leaves the stack version it carried stale, and that is an
		// optimistic-concurrency conflict rather than a verdict. Discovery is
		// sweeping constantly during these cells, so it is common enough that not
		// retrying reported three charts as hard failures with no diagnostic —
		// there was none to give, because nothing had run.
		if !retry || state != "Rejected" || attempt == applyAttempts {
			return state, message
		}
		// Backoff, not a fixed pause. The guard fires when a background sync
		// lands between the CLI's read and its submit, so the odds depend on how
		// long a sync of this resource takes — and that scales with the chart.
		// A fixed 10s and four tries was enough for a small chart and not for
		// external-secrets, which ships two dozen CRDs: same race, longer window,
		// and it exhausted every attempt.
		backoff := time.Duration(attempt*attempt) * 5 * time.Second
		f.t.Logf("apply --mode %s was rejected (attempt %d/%d); retrying in %s",
			mode, attempt, applyAttempts, backoff)
		time.Sleep(backoff)
	}
}

func (f *formaeCLI) applyOnce(mode, forma string) (state string, message string) {
	f.t.Helper()
	out, _ := runCmdCombined(f.binary, f.args("apply", "--mode", mode, "--yes", forma)...)

	// An apply whose forma already matches reality submits no command at all and
	// says so. That is a success, not a missing command — and it is the normal
	// case for the bootstrap, whose target is shared across runs.
	if strings.Contains(out, "No changes needed") {
		return "Success", ""
	}

	// A refusal can also happen before anything is submitted. The drift guard
	// turns down a reconcile against a stack that changed since the last one,
	// and the CLI reports that instead of a command id. Treating a missing id
	// as a harness error made the guard doing its job look like the test
	// falling over — which is how "the guard never fires on reconcile" got
	// believed for a while. It fires; nothing was listening.
	if strings.Contains(out, "rejected because") || strings.Contains(out, "modified since the last reconcile") {
		return "Rejected", strings.TrimSpace(firstMeaningfulLine(out))
	}

	match := commandIDPattern.FindStringSubmatch(out)
	if match == nil {
		f.t.Fatalf("no command submitted by `apply --mode %s`: %s", mode, strings.TrimSpace(out))
	}
	return f.waitCommand(match[1])
}

// commandTimeout bounds how long a submitted command may take to settle.
//
// Three times the readiness cap, because a formae command is strictly larger
// than the Helm operation inside it: the plugin waits for the chart's objects,
// and the agent polls, records and persists around that.
//
// Two is not enough. vault's upgrade — a StatefulSet with volumes plus an
// agent-injector — took 12m15s against a 12m cap, and being fifteen seconds
// short reported as a hang rather than as slowness.
func commandTimeout() time.Duration {
	return 3 * interopInstallTimeout()
}

func (f *formaeCLI) waitCommand(id string) (state string, message string) {
	f.t.Helper()
	deadline := time.Now().Add(commandTimeout())
	for time.Now().Before(deadline) {
		raw, err := runCmd(f.binary, f.args("status", "command",
			"--query=id:"+id, "--output-consumer", "machine")...)
		if err == nil {
			var payload struct {
				Commands []struct {
					State           string
					ErrorMessage    string
					ResourceUpdates []struct {
						ResourceType string
						ErrorMessage string
						State        string
						Operation    string
					}
				}
			}
			if json.Unmarshal([]byte(raw), &payload) == nil && len(payload.Commands) > 0 {
				cmd := payload.Commands[0]
				// Recorded on every poll, not just terminal ones, so a timeout can
				// say what it was waiting on. It previously reported an empty
				// state for a command that was plainly InProgress, which reads as
				// "the command vanished" rather than "it was still working".
				state = cmd.State
				switch cmd.State {
				case "Success", "Failed", "Rejected", "Canceled":
					message = cmd.ErrorMessage
					// The per-resource State is the useful one: a command reports
					// Failed either way, but the update underneath says whether it
					// was Rejected before the plugin ran or failed inside it. The
					// machine output carries no error text for either, so the
					// state is most of what there is to go on.
					outcome := cmd.State
					for _, update := range cmd.ResourceUpdates {
						if !strings.HasSuffix(update.ResourceType, "Release") {
							continue
						}
						if update.ErrorMessage != "" {
							message = update.ErrorMessage
						}
						if update.State == "Rejected" {
							outcome = "Rejected"
						}
						if message == "" {
							message = fmt.Sprintf("resource update %s with no diagnostic", update.State)
						}
					}
					return outcome, message
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	f.t.Fatalf("command %s did not settle within %s (last state %q)", id, commandTimeout(), state)
	return "", ""
}

// Extract writes a forma describing resources matching query.
func (f *formaeCLI) Extract(query, path string) error {
	_, err := runCmd(f.binary, f.args("extract", "--query", query,
		"--schema-location", "local", "--yes", path)...)
	return err
}

// Resource returns the inventory entry for one native id, or nil.
//
// Keyed by native id rather than "the first Release in the inventory": this runs
// against whatever cluster is configured, which may hold releases belonging to
// other work.
func (f *formaeCLI) Resource(resourceType, nativeID string) map[string]any {
	raw, err := runCmd(f.binary, f.args("inventory", "resources",
		"--query", "type:"+resourceType, "--output-consumer", "machine")...)
	if err != nil {
		return nil
	}
	var payload struct {
		Resources []map[string]any
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return nil
	}
	for _, res := range payload.Resources {
		id, _ := res["NativeID"].(string)
		if id == nativeID {
			return res
		}
	}
	return nil
}

func (f *formaeCLI) Destroy(stack string) {
	_, _ = runCmdCombined(f.binary, f.args("destroy", "--yes", "--query", "stack:"+stack)...)
}

// ---------------------------------------------------------------------------
// Shelling out
// ---------------------------------------------------------------------------

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func runCmdCombined(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
