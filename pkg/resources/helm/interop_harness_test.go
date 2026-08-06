//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Support for the formae<->Helm interop test: chart specs, the two Helm drivers,
// and the formae CLI wrapper. The scenario itself is in
// interop_integration_test.go.
package helm

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
	"helm.sh/helm/v3/pkg/release"
	"sigs.k8s.io/yaml"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

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
// Repo-root testdata, matching chartPath's `../../../testdata/charts/hooked`
// rather than a package-local testdata dir — the conformance fixtures live
// there too, so charts stay in one place.
var specDir = filepath.Join("..", "..", "..", "testdata", "interop")

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

func newHelmDriver(t *testing.T, cfg *config.Config) helmClient {
	t.Helper()
	if strings.EqualFold(os.Getenv("INTEROP_HELM"), "cli") {
		return &cliHelm{t: t}
	}
	return &sdkHelm{t: t, cfg: cfg}
}

// --- SDK driver -------------------------------------------------------------

type sdkHelm struct {
	t   *testing.T
	cfg *config.Config
}

func (h *sdkHelm) Name() string { return "sdk" }

func (h *sdkHelm) config(namespace string) (*action.Configuration, error) {
	return newActionConfig(h.cfg, namespace)
}

func (h *sdkHelm) Install(spec chartSpec, namespace, release string) error {
	conf, err := h.config(namespace)
	if err != nil {
		return err
	}

	// loadChart is the provisioner's own, so the chart this test installs is
	// resolved exactly the way a formae-driven install would resolve it.
	chart, err := loadChart(conf, &releaseProperties{
		Chart:   spec.Chart,
		RepoURL: spec.RepoURL,
		Version: spec.Version,
	})
	if err != nil {
		return fmt.Errorf("load chart: %w", err)
	}

	install := action.NewInstall(conf)
	install.ReleaseName = release
	install.Namespace = namespace
	install.CreateNamespace = true
	install.Version = spec.Version
	install.Wait = true
	install.Timeout = 10 * time.Minute

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
	rollback.Timeout = 10 * time.Minute
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
		"--wait", "--timeout", "10m",
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
		"-n", namespace, "--wait", "--timeout", "10m")
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
		Revision int    `json:"revision"`
		Status   string `json:"status"`
		Chart    string `json:"chart"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, err
	}
	states := make([]releaseState, 0, len(rows))
	for _, row := range rows {
		states = append(states, releaseState{
			// "<name>-<version>", and chart names contain hyphens while versions
			// do not, so the split is on the last one.
			Version:  row.Chart[strings.LastIndex(row.Chart, "-")+1:],
			Revision: row.Revision,
			Status:   row.Status,
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
	return &formaeCLI{t: t, binary: binary}
}

var commandIDPattern = regexp.MustCompile(`id:([A-Za-z0-9]+)`)

// Apply submits a forma and waits for it to settle. Returns the first
// release-scoped error message, which is empty on success.
//
// `formae apply` takes the forma positionally and exits 0 even when it rejects
// one, putting the reason on stderr — so both streams are read and the exit code
// is not the signal.
func (f *formaeCLI) Apply(mode, forma string) (state string, message string) {
	f.t.Helper()
	out, _ := runCmdCombined(f.binary, "apply", "--mode", mode, "--yes", forma)

	// An apply whose forma already matches reality submits no command at all and
	// says so. That is a success, not a missing command — and it is the normal
	// case for the bootstrap, whose target is shared across runs.
	if strings.Contains(out, "No changes needed") {
		return "Success", ""
	}

	match := commandIDPattern.FindStringSubmatch(out)
	if match == nil {
		f.t.Fatalf("no command submitted by `apply --mode %s`: %s", mode, strings.TrimSpace(out))
	}
	return f.waitCommand(match[1])
}

func (f *formaeCLI) waitCommand(id string) (state string, message string) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		raw, err := runCmd(f.binary, "status", "command",
			"--query=id:"+id, "--output-consumer", "machine")
		if err == nil {
			var payload struct {
				Commands []struct {
					State           string
					ErrorMessage    string
					ResourceUpdates []struct {
						ResourceType string
						ErrorMessage string
					}
				}
			}
			if json.Unmarshal([]byte(raw), &payload) == nil && len(payload.Commands) > 0 {
				cmd := payload.Commands[0]
				switch cmd.State {
				case "Success", "Failed", "Rejected", "Canceled":
					message = cmd.ErrorMessage
					for _, update := range cmd.ResourceUpdates {
						if strings.HasSuffix(update.ResourceType, "Release") && update.ErrorMessage != "" {
							message = update.ErrorMessage
						}
					}
					return cmd.State, message
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	f.t.Fatalf("command %s never finished", id)
	return "", ""
}

// Extract writes a forma describing resources matching query.
func (f *formaeCLI) Extract(query, path string) error {
	_, err := runCmd(f.binary, "extract", "--query", query,
		"--schema-location", "local", "--yes", path)
	return err
}

// Resource returns the inventory entry for one native id, or nil.
//
// Keyed by native id rather than "the first Release in the inventory": this runs
// against whatever cluster is configured, which may hold releases belonging to
// other work.
func (f *formaeCLI) Resource(resourceType, nativeID string) map[string]any {
	raw, err := runCmd(f.binary, "inventory", "resources",
		"--query", "type:"+resourceType, "--output-consumer", "machine")
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
	_, _ = runCmdCombined(f.binary, "destroy", "--yes", "--query", "stack:"+stack)
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
