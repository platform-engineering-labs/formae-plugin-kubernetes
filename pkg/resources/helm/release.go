// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"bytes"
	"context"
	"encoding/json"
	goerrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

const ResourceTypeRelease = "K8S::Helm::Release"

// defaultTimeoutSeconds bounds a single Helm operation. Generous because Helm
// blocks on hook completion regardless of the Wait flag, and a chart's
// pre-install migration Job legitimately takes minutes.
const defaultTimeoutSeconds = 600

func init() {
	registry.Register(
		ResourceTypeRelease,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationCheckStatus,
			resource.OperationList,
		},
		func(client *transport.Client, cfg *config.Config) prov.Provisioner {
			return &Release{Client: client, Config: cfg}
		},
	)
}

// Release provisions K8S::Helm::Release by driving the Helm SDK.
//
// Helm owns the objects the chart renders; formae owns the release. This is the
// opposite of the HelmChart render-and-decompose path, and it is deliberate:
// hooks, hook ordering, hook delete policies, CRD install ordering and release
// history are Helm's to implement, and reimplementing them in formae means
// reimplementing Helm.
type Release struct {
	Client *transport.Client
	Config *config.Config
}

var _ prov.Provisioner = &Release{}

// releaseProperties is the resource's PKL-facing shape.
type releaseProperties struct {
	Metadata releaseMetadata `json:"metadata"`

	// omitempty is load-bearing, not tidiness: Read cannot recover the chart
	// reference, so it must be *absent* from the reported state. Without
	// omitempty the zero value is marshalled as "chart":"" and overwrites the
	// desired value in formae's state, and every later plain apply is then
	// refused as drift. repoURL has always had it, which is why that field
	// never had the problem.
	Chart           string         `json:"chart,omitempty"`
	RepoURL         string         `json:"repoURL,omitempty"`
	Version         string         `json:"version,omitempty"`
	Values          map[string]any `json:"values,omitempty"`
	CreateNamespace bool           `json:"createNamespace,omitempty"`
	SkipCrds        bool           `json:"skipCrds,omitempty"`
	DisableHooks    bool           `json:"disableHooks,omitempty"`
	Atomic          bool           `json:"atomic,omitempty"`
	TimeoutSeconds  int            `json:"timeoutSeconds,omitempty"`

	// Computed. revision/status/appVersion mirror the release record;
	// resourceNames is the rendered inventory, so a collapsed release still
	// tells the user exactly what it manages.
	Revision      int                 `json:"revision,omitempty"`
	Status        string              `json:"status,omitempty"`
	AppVersion    string              `json:"appVersion,omitempty"`
	ResourceNames map[string][]string `json:"resourceNames,omitempty"`
}

type releaseMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func (p *releaseProperties) timeout() time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return defaultTimeoutSeconds * time.Second
}

// ---------------------------------------------------------------------------
// Request IDs
// ---------------------------------------------------------------------------

// opKind distinguishes what a Status poll is waiting for. StatusRequest carries
// no operation field, so the RequestID has to say. Encoding it keeps Status
// stateless: everything it needs is in the ID plus the cluster.
type opKind string

const (
	opInstall opKind = "install"
	opUpgrade opKind = "upgrade"
	opDelete  opKind = "delete"
)

// requestID formats "<namespace>/<name>@<revision>:<op>".
//
// This, not the NativeID, is the durable handle for an in-flight operation.
// Create withholds the NativeID until the release is fully deployed, so Status
// has to locate the release from here. The host preserves RequestID verbatim
// across every poll of one operation (plugin_operator.go:238-248), which is what
// makes that safe.
func requestID(namespace, name string, revision int, op opKind) string {
	return fmt.Sprintf("%s/%s@%d:%s", namespace, name, revision, op)
}

func parseRequestID(id string) (namespace, name string, revision int, op opKind, err error) {
	at := strings.LastIndex(id, "@")
	colon := strings.LastIndex(id, ":")
	if at < 0 || colon < at {
		return "", "", 0, "", fmt.Errorf("malformed request id %q", id)
	}
	revision, err = strconv.Atoi(id[at+1 : colon])
	if err != nil {
		return "", "", 0, "", fmt.Errorf("malformed revision in request id %q: %w", id, err)
	}
	namespace, name, err = prov.ParseNamespacedNativeID(id[:at])
	if err != nil {
		return "", "", 0, "", fmt.Errorf("malformed target in request id %q: %w", id, err)
	}
	return namespace, name, revision, opKind(id[colon+1:]), nil
}

// ---------------------------------------------------------------------------
// Ownership
// ---------------------------------------------------------------------------

// formaeManagedLabel marks a release as one this plugin installed.
//
// Stored as a Helm release label, which the secrets driver persists on the
// release Secret and restores on read (driver/secrets.go:76). Crucially Helm
// carries release labels forward: `upgrade` merges the previous release's labels
// under the new ones (upgrade.go:300) and `rollback` copies them
// (rollback.go:155). So the marker survives an out-of-band `helm upgrade` or
// `helm rollback` — it identifies the release lineage, not a single revision.
const formaeManagedLabel = "formae.dev/managed"

// formaeLabelPrefix covers this plugin's own bookkeeping labels. They are
// written onto the release record and must never be reported back as part of
// the resource: they are not in the user's forma, so surfacing them makes every
// later plain apply look like drift and `formae extract` writes them into the
// generated file.
const formaeLabelPrefix = "formae.dev/"

// formaeTimeoutLabel records the operation's timeout on the release itself.
//
// Stored in the cluster rather than in this process because that is the only
// place that survives a restart, and Status has no access to the forma: it is
// handed a RequestID and nothing else. Without it stalled() has to guess with
// the package default, and a release given a longer timeoutSeconds is declared
// dead while its own install is still running.
const formaeTimeoutLabel = "formae.dev/timeout-seconds"

// releaseLabels stamps the plugin's bookkeeping onto the user's release labels.
// Helm rejects its own reserved names (name, owner, status, version, createdAt,
// modifiedAt), which these keys are not.
func releaseLabels(props *releaseProperties) map[string]string {
	labels := make(map[string]string, len(props.Metadata.Labels)+2)
	// Defensive: a forma extracted by an older build may still carry Helm's
	// reserved names, and Helm rejects the whole operation if it sees one.
	for k, v := range withoutSystemLabels(props.Metadata.Labels) {
		labels[k] = v
	}
	labels[formaeManagedLabel] = "true"
	labels[formaeTimeoutLabel] = strconv.Itoa(int(props.timeout().Seconds()))
	return labels
}

// withoutFormaeLabels drops the plugin's own bookkeeping. Returns nil rather
// than an empty map, so a release carrying nothing else reports no labels at
// all instead of a value that reads as a change against a forma with none.
func withoutFormaeLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, formaeLabelPrefix) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// releaseTimeout recovers the timeout the operation was started with.
//
// Falls back to the package default for a release installed before the label
// existed, for a foreign release, and for a value that will not parse — never
// into an unbounded window, because the whole point of the timeout is to bound
// how long a wedged release goes unreported.
func releaseTimeout(rel *release.Release) time.Duration {
	if secs, err := strconv.Atoi(rel.Labels[formaeTimeoutLabel]); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultTimeoutSeconds * time.Second
}

// withoutSystemLabels drops Helm's own bookkeeping labels.
//
// Needed because the secrets driver only filters them on Get; the list and last
// paths hand them back verbatim (driver/secrets.go:103,141). Left in place they
// leak into Read, get copied into an extracted forma, and Helm then rejects the
// next upgrade with "user supplied labels contains system reserved label name".
func withoutSystemLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	system := make(map[string]struct{}, 8)
	for _, k := range driver.GetSystemLabels() {
		system[k] = struct{}{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if _, reserved := system[k]; reserved {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// formaeOwns reports whether this plugin installed the release.
func formaeOwns(rel *release.Release) bool {
	return rel != nil && rel.Labels[formaeManagedLabel] == "true"
}

// ---------------------------------------------------------------------------
// Deciding what to do about an existing release
// ---------------------------------------------------------------------------

// submitAction is what submit should do given whatever release already exists.
type submitAction int

const (
	// actionInstall — no release under this name.
	actionInstall submitAction = iota
	// actionUpgrade — a settled release (deployed, failed or superseded). Helm
	// accepts an upgrade from any of those.
	actionUpgrade
	// actionRetry — another Helm operation holds the lock. Come back later.
	actionRetry
	// actionRecover — this plugin owns the release and is running no operation
	// for it, so the pending record is a lock nothing will ever release.
	// Rewriting it is the only way anything else can happen.
	actionRecover
	// actionBlocked — the same wedge on a release this plugin did not install.
	// Reported, never rewritten: another tool's record is not ours to edit.
	actionBlocked
	// actionForeign — a release under this name exists and this plugin did not
	// install it. Taking it over would rewrite someone else's release.
	actionForeign
	// actionRejoin — the pending release is an operation THIS process started
	// with this exact desired state, and it is still running. Report progress
	// against it instead of refusing the call.
	actionRejoin
	// actionSettled — the desired state is already deployed. Nothing to do.
	actionSettled
)

// planSubmit decides the operation and the revision it should produce.
//
// Split out from submit so the decision is testable without a cluster: the
// pending and foreign branches are exactly where this went wrong before, and
// they are the hardest states to reach on demand against a live Helm.
//
// isCreate says formae has no NativeID for this resource, i.e. it believes the
// release does not exist. That is what makes a collision detectable — but it is
// not sufficient on its own, because Create withholds the NativeID until the
// release is fully deployed, so formae also retries as Create after a failed
// first install. The ownership marker separates "our own abandoned attempt",
// which we may take over, from a genuinely foreign release, which we may not.
//
// flight is this process's record of an operation already running for the
// release, or nil. want is the desired state the caller is submitting, or nil
// when the caller has none to compare (only the tests). Both may be absent and
// the decision then degrades to the cluster-only one.
func planSubmit(
	current *release.Release,
	isCreate bool,
	flight *inflight,
	want *releaseProperties,
) (action submitAction, targetRevision int) {
	if current == nil {
		return actionInstall, 1
	}
	if releaseIsPending(current) {
		if rejoinable(current, flight, want) {
			return actionRejoin, current.Version
		}
		if abandoned(current, flight) {
			return actionRecover, current.Version
		}
		// Not ours, and stuck long past any plausible hook runtime. Reported
		// rather than rewritten: another tool's release record is not ours to
		// edit, however dead its operation looks.
		if stalled(current, flight) {
			return actionBlocked, current.Version
		}
		return actionRetry, current.Version
	}
	if settled(current, want) {
		return actionSettled, current.Version
	}
	if isCreate && !formaeOwns(current) {
		return actionForeign, current.Version
	}
	return actionUpgrade, current.Version + 1
}

// rejoinable reports whether the pending release is this process's own
// in-flight operation for exactly this desired state.
//
// This is the narrow reading of the rule above the actionRetry branch in
// submit, not a repeal of it. That rule is right that handing back InProgress
// for an in-flight revision reports success for work that never ran — but only
// when the in-flight work is a DIFFERENT change. When the fingerprints match,
// the work in flight is the work being asked for, and rejoining is the honest
// answer. Every other case still takes the conservative path.
//
// It matters because the agent re-drives rather than resumes: on restart it
// resets an InProgress resource update to NotStarted and calls Create again
// (formae metastructure.go:1262), while this process's goroutine carries on.
func rejoinable(current *release.Release, flight *inflight, want *releaseProperties) bool {
	if flight == nil || want == nil {
		return false
	}
	// Past its deadline the goroutine's context is cancelled; it is unwinding,
	// not working.
	if !flight.deadline.IsZero() && time.Now().After(flight.deadline) {
		return false
	}
	if flight.revision != current.Version {
		return false
	}
	asked := fingerprint(want)
	return asked != "" && asked == flight.fingerprint
}

// abandoned reports whether a pending release has no operation behind it, so
// its record can be rewritten rather than waited on.
//
// For a release this plugin installed, that is a fact rather than an inference.
// Helm operations for formae run in exactly one place — this process — so an
// empty in-flight registry means nothing is driving this release. No clock is
// needed and none is used: recovery is immediate on the next call.
//
// The one case that clock still has to cover is a release we do NOT own. There
// the Helm CLI, another tool or a second agent may legitimately be mid-operation
// and we have no way to see it, so the wide window stands — better to leave
// somebody else's release wedged than to rewrite the record of a live install.
//
// A caveat worth stating rather than burying: two formae agents driving the same
// cluster would each read the other's in-flight install of a shared release as
// abandoned. Formae's own command locking keeps that from arising within one
// agent; across agents it is a genuine multi-writer setup and out of scope here.
func abandoned(current *release.Release, flight *inflight) bool {
	// This process is running something for the release. Even when it is a
	// different desired state than the one being asked for, the operation is
	// live and its record is not ours to rewrite.
	if flight != nil {
		return false
	}
	return formaeOwns(current)
}

// settled reports whether the desired state is already live, making the
// operation a no-op.
//
// Needed because a crash is not the only way formae asks for work that is
// already done: after an agent restart the re-driven Create arrives against a
// release that finished while the agent was down. Without this it plans an
// upgrade, bumping the revision for nothing and re-running the chart's
// pre-upgrade and post-upgrade hooks — a second database migration, on a chart
// like kratos.
//
// Deliberately strict, because concluding "settled" wrongly drops a change
// silently. Under these conditions that cannot happen: if the pinned version
// and the values both already match what is deployed, there is no change to
// drop. The residual is a chart whose content moved under a fixed version tag,
// which storedChartUsable already assumes away.
func settled(current *release.Release, want *releaseProperties) bool {
	if want == nil || !formaeOwns(current) {
		return false
	}
	if current.Info == nil || current.Info.Status != release.StatusDeployed {
		return false
	}
	// Carries the "explicit version, matching chart name" guard, including the
	// refusal to conclude anything about an unpinned version.
	if !storedChartUsable(current, want) {
		return false
	}
	return valuesEqual(current.Config, want.Values)
}

// valuesEqual compares two value trees as canonical JSON.
//
// Not reflect.DeepEqual: the release record and the request reach us through
// different decoders, so the same number can be an int on one side and a
// float64 on the other. Marshalling normalises that, and encoding/json sorts
// map keys, so nested ordering cannot produce a spurious difference either.
func valuesEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(encodedA, encodedB)
}

// storedChartUsable reports whether the chart already in the release record
// satisfies what the forma asks for, making a fetch unnecessary.
//
// Helm persists the whole chart — templates included — in the release Secret;
// that is how `helm rollback` re-renders without network access. So re-applying
// the version that is already deployed needs nothing fetched, and therefore needs
// no repoURL. That matters for adoption: an extracted forma describes the live
// release exactly, so it can be adopted with no repository information at all,
// which is just as well because Helm does not record where a chart came from.
//
// Deliberately requires an explicit version. An empty version means "newest at
// apply time", and reusing the stored chart then would silently pin the release
// to its current version and never upgrade it again.
func storedChartUsable(current *release.Release, props *releaseProperties) bool {
	if current == nil || current.Chart == nil || current.Chart.Metadata == nil {
		return false
	}
	if props.Version == "" || props.Version != current.Chart.Metadata.Version {
		return false
	}
	// The chart reference may be a bare name, a repo-qualified name, an oci://
	// URL or a path; all that can be compared against the record is the name.
	return chartRefName(props.Chart) == current.Chart.Metadata.Name
}

// chartRefName reduces a chart reference to its chart name.
func chartRefName(ref string) string {
	ref = strings.TrimSuffix(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// ---------------------------------------------------------------------------
// Create / Update
// ---------------------------------------------------------------------------

func (r *Release) Create(ctx context.Context, request *resource.CreateRequest) (*resource.CreateResult, error) {
	props, err := decodeProperties(request.Properties)
	if err != nil {
		return nil, err
	}
	// withholdNativeID: formae records a resource once it has a NativeID, so
	// handing one back at pending-install would put a half-installed release
	// under management. Status supplies it after the release is fully deployed.
	pr, err := r.submit(ctx, props, true)
	if err != nil {
		return nil, err
	}
	pr.Operation = resource.OperationCreate
	return &resource.CreateResult{ProgressResult: pr}, nil
}

func (r *Release) Update(ctx context.Context, request *resource.UpdateRequest) (*resource.UpdateResult, error) {
	props, err := decodeProperties(request.DesiredProperties)
	if err != nil {
		return nil, err
	}
	// Update keeps returning the NativeID: the resource is already in formae's
	// state, so there is nothing new to record and withholding it would only
	// risk the updater losing the handle mid-upgrade.
	pr, err := r.submit(ctx, props, false)
	if err != nil {
		return nil, err
	}
	pr.Operation = resource.OperationUpdate
	return &resource.UpdateResult{ProgressResult: pr}, nil
}

// submit starts an install or upgrade and returns immediately with InProgress.
//
// Helm has no server-side operation controller — the work runs in this process.
// But the authoritative state is the release record in the cluster, so the
// goroutine holds nothing that has to survive: if the plugin dies, Status reads
// the record and reports a stalled operation. Blocking here instead would give
// exactly the same wedge whenever formae's call deadline fired first, minus any
// progress reporting.
// isCreate is true when called from Create: formae has no NativeID for this
// resource yet, which is both why the NativeID is withheld and how a collision
// with a pre-existing release becomes detectable.
func (r *Release) submit(
	ctx context.Context,
	props *releaseProperties,
	isCreate bool,
) (*resource.ProgressResult, error) {
	ns := props.Metadata.Namespace
	name := props.Metadata.Name
	if ns == "" || name == "" {
		return nil, fmt.Errorf("%s: metadata.name and metadata.namespace are required", ResourceTypeRelease)
	}

	conf, err := newActionConfig(r.Config, ns)
	if err != nil {
		return nil, err
	}

	current, err := lastRelease(conf, name)
	if err != nil {
		return nil, err
	}

	flight := lookupFlight(r.Config, ns, name)
	action, target := planSubmit(current, isCreate, flight, props)

	if action == actionRecover {
		// Clear the abandoned lock, then decide again from the record we just
		// wrote. Re-planning rather than branching gets every case right for
		// free: a recovered-as-deployed release whose desired state matches is
		// settled, one whose desired state has since moved is an upgrade, and a
		// recovered-as-failed release is an upgrade too.
		if _, err := recoverAbandoned(ctx, conf, r.Client, current); err != nil {
			return nil, err
		}
		if current, err = lastRelease(conf, name); err != nil {
			return nil, err
		}
		if current == nil {
			return nil, fmt.Errorf("release %s/%s vanished while being recovered", ns, name)
		}
		action, target = planSubmit(current, isCreate, flight, props)
	}

	switch action {
	case actionRejoin:
		// Our own operation, still running in this process, for this exact
		// desired state. requestID is a pure function of these inputs, so the
		// ID handed back here is the one the interrupted call already returned,
		// and Status carries on polling the operation that never stopped.
		return &resource.ProgressResult{
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        nativeIDUnless(isCreate, ns, name),
			RequestID:       requestID(ns, name, target, flight.op),
			StatusMessage: fmt.Sprintf(
				"rejoined the %s of %s/%s already running in this plugin (started %s ago)",
				flight.op, ns, name, time.Since(flight.started).Truncate(time.Second)),
		}, nil

	case actionSettled:
		// Already deployed at the desired version and values, so run no Helm
		// operation — but do not claim success outright either. The objects are
		// only known to have been accepted by the apiserver; whether they are
		// ready is exactly what Status already knows how to answer, and it will
		// report Success with the properties once they are.
		//
		// The op only decides whether Status withholds the NativeID until the
		// objects are ready, which is what a Create wants and an Update does
		// not — the same split nativeIDUnless makes.
		settledOp := opUpgrade
		if isCreate {
			settledOp = opInstall
		}
		return &resource.ProgressResult{
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        nativeIDUnless(isCreate, ns, name),
			RequestID:       requestID(ns, name, target, settledOp),
			StatusMessage: fmt.Sprintf(
				"release %s/%s is already at the desired state (revision %d); checking readiness",
				ns, name, current.Version),
		}, nil

	case actionRetry:
		// Helm's pending status is a pessimistic lock — both
		// Install.availableName and Upgrade.prepareUpgrade refuse to proceed —
		// so there is nothing to do but come back later.
		//
		// This MUST be a retryable failure, not InProgress. Reporting InProgress
		// here would hand back a RequestID naming the revision already in flight;
		// once that revision settled, Status would see the release deployed at
		// the expected revision and report Success for work that never ran,
		// silently dropping the change. ResourceConflict is on the SDK's
		// recoverable list, so the agent re-drives the whole operation instead.
		return &resource.ProgressResult{
			OperationStatus: resource.OperationStatusFailure,
			ErrorCode:       resource.OperationErrorCodeResourceConflict,
			NativeID:        nativeIDUnless(isCreate, ns, name),
			StatusMessage: fmt.Sprintf(
				"release %s/%s is %s from another operation; retrying once Helm releases the lock",
				ns, name, current.Info.Status),
		}, nil

	case actionForeign:
		// Mirrors the guard the render-and-decompose path had. Overwriting the
		// release record of a release formae did not create would rewrite its
		// history, and `helm rollback` would then roll back to revisions this
		// forma never described.
		//
		// Returned as a Go error, not a Failure ProgressResult: the host records
		// ErrorMessage for the former and drops StatusMessage for the latter, and
		// a refusal the operator cannot read is not actionable. Terminal either
		// way — AlreadyExists is not on the recoverable list.
		return nil, fmt.Errorf(
			"release %s/%s already exists at revision %d and was not created by formae: "+
				"applying would overwrite that record and destroy its rollback history. "+
				"Adopt it first with `formae extract --query 'type:%s managed:false'`, "+
				"or choose a different metadata.name",
			ns, name, current.Version, ResourceTypeRelease)

	case actionRecover:
		// Unreachable: recovery happens before the switch and re-plans. Kept so
		// adding an action cannot silently fall through to an install.
		return nil, goerrors.New(stalledMessage(current, ns, name))
	}

	// Reuse the stored chart when the forma asks for what is already deployed,
	// so re-applying an unchanged release needs no repository access.
	var chrt *chart.Chart
	if storedChartUsable(current, props) {
		chrt = current.Chart
	} else {
		chrt, err = loadChart(conf, props)
		if err != nil {
			return nil, err
		}
	}

	// Detached: the request context is cancelled the moment submit returns.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), props.timeout())

	op := opInstall
	if action == actionUpgrade {
		op = opUpgrade
	}

	// Registered before the goroutine starts, so a re-driven call cannot slip
	// between the two and miss the operation it should be rejoining. The cancel
	// func is what lets a graceful stop turn `pending-install` into `failed`.
	registerFlight(r.Config, ns, name, inflight{
		op:          op,
		revision:    target,
		fingerprint: fingerprint(props),
		deadline:    time.Now().Add(props.timeout()),
	}, cancel)

	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer removeFlight(r.Config, ns, name)
		defer invalidateInventory(r.Config)
		if op == opInstall {
			done <- runInstall(runCtx, conf, props, chrt)
			return
		}
		done <- runUpgrade(runCtx, conf, props, chrt)
	}()

	// Do not return until Helm has written the release record, or has failed
	// before writing it.
	//
	// Two reasons. Status is stateless — it reads the record and nothing else —
	// so returning InProgress before the record exists makes the very first poll
	// see "not found" and call the operation dead. And a failure that happens
	// before the record (bad values, template error, unreachable apiserver)
	// leaves no trace in the cluster for Status to report, so it has to surface
	// here or not at all.
	if err := awaitRecorded(runCtx, conf, name, target, done); err != nil {
		return nil, err
	}

	return &resource.ProgressResult{
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        nativeIDUnless(isCreate, ns, name),
		RequestID:       requestID(ns, name, target, op),
	}, nil
}

// nativeIDUnless returns the native id, or "" when it is being withheld until
// the release is fully deployed.
func nativeIDUnless(withhold bool, namespace, name string) string {
	if withhold {
		return ""
	}
	return prov.NativeID(namespace, name)
}

// recordTimeout bounds the wait for Helm to write the release record.
//
// Helm writes it after rendering, namespace creation and `crds/` installation
// but before any hook runs (install.go:403, upgrade.go:376), so this only has to
// cover that prefix — not hook execution, which is what Status polls for.
const recordTimeout = 60 * time.Second

// awaitRecorded blocks until the release reaches the target revision in Helm's
// storage, or the operation fails first.
func awaitRecorded(
	ctx context.Context,
	conf *action.Configuration,
	name string,
	target int,
	done <-chan error,
) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(recordTimeout)
	defer deadline.Stop()

	for {
		select {
		case err := <-done:
			// The operation finished before we observed the record — either it
			// failed early, or the chart was small enough to complete outright.
			return err

		case <-tick.C:
			rel, err := lastRelease(conf, name)
			if err != nil {
				return err
			}
			if rel != nil && rel.Version >= target {
				return nil
			}

		case <-deadline.C:
			return fmt.Errorf(
				"helm did not record release %q at revision %d within %s",
				name, target, recordTimeout)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runInstall executes helm install.
func runInstall(ctx context.Context, conf *action.Configuration, props *releaseProperties, chrt *chart.Chart) error {
	inst := action.NewInstall(conf)
	inst.ReleaseName = props.Metadata.Name
	inst.Namespace = props.Metadata.Namespace
	inst.CreateNamespace = props.CreateNamespace
	inst.SkipCRDs = props.SkipCrds
	inst.DisableHooks = props.DisableHooks
	inst.Atomic = props.Atomic
	inst.Timeout = props.timeout()
	// Carries the ownership marker; see formaeManagedLabel.
	inst.Labels = releaseLabels(props)

	// Readiness is Status's job, polled from the cluster. Letting Helm block on
	// Wait would defeat the async model. Note this does NOT make the call
	// non-blocking for charts with hooks — Helm waits on hook Jobs regardless.
	inst.Wait = false

	_, err := inst.RunWithContext(ctx, chrt, props.Values)
	return err
}

func runUpgrade(ctx context.Context, conf *action.Configuration, props *releaseProperties, chrt *chart.Chart) error {
	up := action.NewUpgrade(conf)
	up.Namespace = props.Metadata.Namespace
	up.SkipCRDs = props.SkipCrds
	up.DisableHooks = props.DisableHooks
	up.Atomic = props.Atomic
	up.Timeout = props.timeout()
	up.Labels = releaseLabels(props)
	up.Wait = false

	// MaxHistory bounds the release Secrets Helm accumulates. Unbounded history
	// is a real problem on frequently-reconciled releases — one Secret per
	// revision, forever.
	up.MaxHistory = 10

	_, err := up.RunWithContext(ctx, props.Metadata.Name, chrt, props.Values)
	return err
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func (r *Release) Read(ctx context.Context, request *resource.ReadRequest) (*resource.ReadResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}

	conf, err := newActionConfig(r.Config, ns)
	if err != nil {
		return nil, err
	}

	rel, err := lastRelease(conf, name)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}

	// A pending release is mid-operation, and its record is a snapshot of a
	// moment rather than a state worth storing.
	//
	// Reporting it would put "pending-upgrade" into formae's state, and formae
	// then refuses to queue an update against a resource it believes is
	// in-flight — a rejection that `--force` does not override. So an
	// out-of-band `helm upgrade` observed at the wrong instant would wedge the
	// resource until something else re-synced it. NotStabilized is on the SDK's
	// recoverable list, so the read is simply retried and the previous settled
	// state is kept meanwhile.
	if releaseIsPending(rel) {
		return &resource.ReadResult{
			ResourceType: request.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotStabilized,
		}, nil
	}

	props := propertiesFromRelease(rel)
	encoded, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshal release properties: %w", err)
	}

	return &resource.ReadResult{
		ResourceType: request.ResourceType,
		Properties:   string(encoded),
	}, nil
}

// propertiesFromRelease projects a release record back onto the resource shape.
//
// rel.Config is the user-supplied values only — not merged with chart defaults —
// so it round-trips against desired state instead of producing a permanent diff
// against every default the chart happens to set.
func propertiesFromRelease(rel *release.Release) *releaseProperties {
	props := &releaseProperties{
		Metadata: releaseMetadata{
			Name:      rel.Name,
			Namespace: rel.Namespace,
			Labels:    withoutFormaeLabels(withoutSystemLabels(rel.Labels)),
		},
		Values:        rel.Config,
		Revision:      rel.Version,
		ResourceNames: resourceNames(rel),
	}
	if rel.Info != nil {
		props.Status = rel.Info.Status.String()
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		// version IS recoverable and must always be reported: it is how a
		// Helm-side upgrade is detected as drift.
		props.Version = rel.Chart.Metadata.Version
		props.AppVersion = rel.Chart.Metadata.AppVersion

		// chart is reported only for a release this plugin did NOT install.
		//
		// Helm records the chart's *name*, never the reference that fetched it. So
		// for a release formae owns, reporting the name would answer "podinfo" for
		// a desired "oci://ghcr.io/stefanprodan/charts/podinfo" and every later
		// plain apply would be refused as drift. Omitting it instead lets formae
		// keep the reference the user wrote — the same treatment repoURL has always
		// had.
		//
		// A foreign release has no desired value to keep, and something has to be
		// reported or discovery records it with no chart at all and
		// `formae extract` emits a forma that cannot be applied. The name is the
		// best that can be recovered, and it is what makes adoption work.
		if !formaeOwns(rel) {
			props.Chart = rel.Chart.Metadata.Name
		}
	}
	return props
}

// resourceNames groups the release's rendered objects by "apiVersion/Kind".
// This is what keeps a collapsed release from being an opaque box: the release
// stands in for its objects in discovery, so it has to be able to say which
// objects those are.
func resourceNames(rel *release.Release) map[string][]string {
	inv := newInventory()
	indexManifest(inv, rel.Manifest, rel.Namespace, rel.Name)

	out := map[string][]string{}
	for id := range inv.objects {
		key := id.Kind
		if id.APIVersion != "" {
			key = id.APIVersion + "/" + id.Kind
		}
		out[key] = append(out[key], id.Namespace+"/"+id.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func (r *Release) Delete(ctx context.Context, request *resource.DeleteRequest) (*resource.DeleteResult, error) {
	ns, name, err := prov.ParseNamespacedNativeID(request.NativeID)
	if err != nil {
		return nil, fmt.Errorf("invalid native id %q for %s: %w", request.NativeID, request.ResourceType, err)
	}

	conf, err := newActionConfig(r.Config, ns)
	if err != nil {
		return nil, err
	}

	rel, err := lastRelease(conf, name)
	if err != nil {
		return nil, err
	}
	if rel == nil {
		return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusSuccess,
		}}, nil
	}

	un := action.NewUninstall(conf)
	// Wait for the objects to actually go, not just for the deletes to be
	// accepted. Returning before termination let a destroy-then-apply race the
	// objects it had just asked Kubernetes to remove.
	un.Wait = true
	un.Timeout = defaultTimeoutSeconds * time.Second
	// KeepHistory=false: formae's own state records the intent to delete, so a
	// retained uninstall record buys nothing and makes a later reinstall of the
	// same name need a replace strategy.
	un.KeepHistory = false

	// Fired rather than awaited, for the same reason install is: Helm blocks on
	// pre-delete and post-delete hooks regardless of Wait, so a chart with a
	// teardown hook would otherwise hold the Delete call open for its full
	// duration.
	//
	// The release record is the completion signal, and Helm's ordering makes it
	// an exact one: the record is moved to `uninstalling` before any object is
	// deleted (uninstall.go:119) and purged only after WaitForDelete and the
	// post-delete hooks have finished (uninstall.go:155). So "record gone" means
	// the objects are gone too.
	//
	// Uninstall has no RunWithContext in Helm v3 (helm#12109 is not in), so
	// u.Timeout is what bounds it.
	go func() {
		defer invalidateInventory(r.Config)
		_, _ = un.Run(name)
	}()

	return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
		Operation:       resource.OperationDelete,
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        prov.NativeID(ns, name),
		RequestID:       requestID(ns, name, rel.Version, opDelete),
		StatusMessage:   fmt.Sprintf("uninstalling release %s/%s", ns, name),
	}}, nil
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func (r *Release) Status(ctx context.Context, request *resource.StatusRequest) (*resource.StatusResult, error) {
	// The release is located from the RequestID, not the NativeID: Create
	// withholds the NativeID until the release is fully deployed, so on an
	// install poll the NativeID is empty by design. The RequestID carries
	// namespace, name and target revision and the host preserves it verbatim
	// across every poll of one operation.
	ns, name, wantRevision, op, err := parseRequestID(request.RequestID)
	if err != nil {
		return nil, err
	}

	conf, err := newActionConfig(r.Config, ns)
	if err != nil {
		return nil, err
	}

	rel, err := lastRelease(conf, name)
	if err != nil {
		return nil, err
	}

	// Scoped to this target: the same namespace/name on another cluster is a
	// different release, and its operation says nothing about this one.
	flight := lookupFlight(r.Config, ns, name)

	if op == opDelete {
		return &resource.StatusResult{
			ProgressResult: deleteStatus(rel, flight, ns, name, request.RequestID),
		}, nil
	}
	return r.installStatus(ctx, conf, rel, flight, ns, name, wantRevision, op, request.RequestID)
}

// deleteStatus reports on an uninstall in flight.
//
// The release record's absence is the completion signal: Helm purges it only
// after WaitForDelete and the post-delete hooks are done, so by then the objects
// are gone too.
func deleteStatus(rel *release.Release, flight *inflight, ns, name string, reqID string) *resource.ProgressResult {
	nativeID := prov.NativeID(ns, name)

	// Purged, or KeepHistory left an uninstalled record behind — either way the
	// release is gone.
	if rel == nil || rel.Info == nil || rel.Info.Status == release.StatusUninstalled {
		return &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusSuccess,
		}
	}

	// The mirror of the install case: the record's absence is completion, so a
	// record still present with no operation behind it means the uninstall was
	// abandoned. Reported with a recoverable code so the agent re-drives Delete
	// rather than handing it to an operator — uninstall is idempotent, and Helm
	// picks up from whatever the dead attempt managed to remove.
	if abandoned(rel, flight) {
		return &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusFailure,
			ErrorCode:       resource.OperationErrorCodeResourceConflict,
			NativeID:        nativeID,
			StatusMessage: fmt.Sprintf(
				"uninstall of %s/%s was abandoned mid-flight (release still %s); retrying it",
				ns, name, rel.Info.Status),
		}
	}

	// Not ours, and long stale. Reported rather than re-driven: the remaining
	// objects are whatever Helm managed to delete before it died, and working
	// that out is an operator's job.
	if stalled(rel, flight) {
		return &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusFailure,
			ErrorCode:       resource.OperationErrorCodeGeneralServiceException,
			NativeID:        nativeID,
			StatusMessage: fmt.Sprintf(
				"release %s/%s stuck %s since %s; no Helm process is finishing it. "+
					"Inspect with `helm status %s -n %s`, then `helm uninstall %s -n %s` to clear it",
				ns, name, rel.Info.Status, rel.Info.LastDeployed, name, ns, name, ns),
		}
	}

	return &resource.ProgressResult{
		Operation:       resource.OperationCheckStatus,
		OperationStatus: resource.OperationStatusInProgress,
		RequestID:       reqID,
		NativeID:        nativeID,
		StatusMessage:   fmt.Sprintf("release %s/%s still present (%s)", ns, name, rel.Info.Status),
	}
}

func (r *Release) installStatus(
	ctx context.Context,
	conf *action.Configuration,
	rel *release.Release,
	flight *inflight,
	ns, name string,
	wantRevision int,
	op opKind,
	reqID string,
) (*resource.StatusResult, error) {
	// A release only earns its NativeID once it is deployed and ready, because
	// that is the moment formae records it as managed. Every non-terminal and
	// failed result on the install path therefore reports none.
	//
	// The cost is real and deliberate: a failed first install leaves formae with
	// no handle, so it cannot Delete the wedged release itself and the operator
	// has to run `helm uninstall`. stalledMessage says so. Upgrades are exempt —
	// the resource is already in state, so there is nothing to withhold.
	pendingID := nativeIDUnless(op == opInstall, ns, name)
	nativeID := prov.NativeID(ns, name)

	if rel == nil {
		// The record is written before any hook runs, so its absence this late
		// means the operation never got off the ground.
		return failure(pendingID, resource.OperationErrorCodeNotFound,
			fmt.Sprintf("release %s/%s not found", ns, name)), nil
	}

	// A newer revision means something else upgraded past us. Our operation's
	// outcome is no longer observable, and reporting success would be a lie.
	if rel.Version > wantRevision {
		return failure(pendingID, resource.OperationErrorCodeGeneralServiceException,
			fmt.Sprintf("release %s/%s advanced to revision %d while waiting for %d",
				ns, name, rel.Version, wantRevision)), nil
	}

	if rel.Version < wantRevision {
		return inProgress(pendingID, reqID, nil,
			fmt.Sprintf("waiting for revision %d (at %d)", wantRevision, rel.Version)), nil
	}

	switch {
	case releaseIsPending(rel):
		// Only an operation running in this process justifies reporting
		// progress. Anything else must reach a verdict now.
		if flight != nil {
			return inProgress(pendingID, reqID, nil, fmt.Sprintf("release is %s", rel.Info.Status)), nil
		}

		// Nothing here is driving it, and holding reqID is proof formae started
		// it — the host only ever issues a RequestID for an operation it
		// submitted. That is stronger evidence than the ownership label, and it
		// does not depend on when Helm stamps the record.
		//
		// Whatever happens below, this must not return InProgress again. A
		// pending release with no operation behind it is stuck: Helm refuses
		// install and upgrade alike, so polling would report progress forever on
		// work that will never finish, and the command would never surface to
		// anyone. Recover it, or fail it with the recovery instructions.
		recovered, err := recoverAbandoned(ctx, conf, r.Client, rel)
		if err != nil {
			return failure(pendingID, resource.OperationErrorCodeGeneralServiceException,
				fmt.Sprintf("%s (recovery also failed: %v)", stalledMessage(rel, ns, name), err)), nil
		}

		if recovered == release.StatusDeployed {
			// The install had in fact finished; only the record was lost. Decide
			// again from the record just written, which is no longer pending, so
			// this recurses exactly once and lands in the deployed branch.
			fresh, err := lastRelease(conf, name)
			if err != nil {
				return nil, err
			}
			return r.installStatus(ctx, conf, fresh, flight, ns, name, wantRevision, op, reqID)
		}
		// Marked failed, which an upgrade is allowed to run over. Reported with
		// a recoverable code so the agent re-drives the operation rather than
		// handing the release to an operator: that re-drive plans an upgrade
		// and finishes what was interrupted.
		return failure(pendingID, resource.OperationErrorCodeResourceConflict,
			fmt.Sprintf("release %s/%s was abandoned mid-%s and has been marked failed; "+
				"retrying will upgrade over it", ns, name, rel.Info.Status)), nil

	case rel.Info.Status == release.StatusFailed:
		return failure(pendingID, resource.OperationErrorCodeGeneralServiceException,
			fmt.Sprintf("release %s/%s failed: %s", ns, name, rel.Info.Description)), nil

	case rel.Info.Status == release.StatusDeployed:
		// "deployed" with Wait=false only means the manifests were accepted by
		// the apiserver, not that anything is running. Check the objects.
		ready, msg, err := manifestReady(ctx, conf, r.Client, rel)
		if err != nil {
			return nil, err
		}
		if !ready {
			return inProgress(pendingID, reqID, nil, msg), nil
		}
		props, err := json.Marshal(propertiesFromRelease(rel))
		if err != nil {
			return nil, fmt.Errorf("marshal release properties: %w", err)
		}
		return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
			Operation:          resource.OperationCheckStatus,
			OperationStatus:    resource.OperationStatusSuccess,
			RequestID:          reqID,
			NativeID:           nativeID,
			ResourceProperties: props,
		}}, nil

	default:
		return inProgress(pendingID, reqID, nil, fmt.Sprintf("release is %s", rel.Info.Status)), nil
	}
}

// stalled reports whether a pending release has been pending long enough that no
// Helm process can still be working on it.
//
// From the cluster alone this is a heuristic and it cannot be otherwise: Helm's
// pending status IS the lock, so nothing there distinguishes "process died" from
// "a slow pre-install hook is still running". The window is therefore
// deliberately wide — healing early would mean acting against a live install.
//
// The in-flight registry sharpens it in one direction only. A hit proves this
// process is still running the operation, so the verdict is suppressed. A miss
// proves nothing: the Helm CLI and a second agent are both allowed to exist, so
// it must never be read as evidence that the release is abandoned.
//
// flight is passed in rather than looked up here, because the lookup needs the
// target and a release record does not name one. Callers hold the config.
func stalled(rel *release.Release, flight *inflight) bool {
	if rel.Info == nil {
		return false
	}
	if flight != nil {
		if flight.deadline.IsZero() || time.Now().Before(flight.deadline) {
			return false
		}
	}
	started := rel.Info.LastDeployed.Time
	if started.IsZero() {
		started = rel.Info.FirstDeployed.Time
	}
	if started.IsZero() {
		return false
	}
	return time.Since(started) > 2*releaseTimeout(rel)
}

// recoverAbandoned rewrites the record of a pending release that nothing is
// coming back to finish, and reports the status it settled on.
//
// Helm's pending status is a lock with no owner and no lease. Once the process
// holding it is gone — and `formae agent stop` SIGKILLs this plugin, so that is
// the ordinary case rather than the exotic one — Helm refuses both install and
// upgrade on that release forever. Nothing in Helm clears it; `helm uninstall`
// is the documented way out, and it destroys the objects and re-runs
// pre-install hooks to get back to where it already was.
//
// So the plugin clears it, doing exactly what Helm's own failRelease does when
// it observes a failure: set the status, write the record back. What makes that
// safe rather than reckless is the caller's guard — the release is one this
// plugin installed, and it has been pending for twice its own timeout.
//
// Which status it settles on is decided by the cluster, because a stuck
// pending-install has two completely different realities underneath it. Helm
// writes `deployed` LAST (install.go:489), after the hooks have run and every
// object has been created:
//
//	Releases.Create(pending-install) -> hooks -> create objects -> SetStatus(deployed)
//
// Dying anywhere in that middle stretch leaves an identical record behind,
// whether the work finished or never started. Calling it `failed` when the
// objects are all present and ready would send a completed install through an
// upgrade, re-running pre-upgrade hooks — a second database migration, on a
// chart like kratos — to reach the state it was already in.
func recoverAbandoned(
	ctx context.Context,
	conf *action.Configuration,
	client *transport.Client,
	rel *release.Release,
) (release.Status, error) {
	ready, detail, err := manifestComplete(ctx, conf, client, rel)
	if err != nil {
		return "", err
	}

	status := release.StatusFailed
	message := fmt.Sprintf(
		"formae: recovered a release abandoned in %s; its objects are incomplete (%s), "+
			"so it is marked failed and the next apply upgrades over it",
		rel.Info.Status, detail)
	if ready {
		status = release.StatusDeployed
		message = fmt.Sprintf(
			"formae: recovered a release abandoned in %s; every object it renders is present "+
				"and ready, so the install did complete and only the record was lost",
			rel.Info.Status)
	}

	rel.SetStatus(status, message)
	if err := conf.Releases.Update(rel); err != nil {
		return "", fmt.Errorf("recovering abandoned release %s/%s: %w", rel.Namespace, rel.Name, err)
	}
	return status, nil
}

func stalledMessage(rel *release.Release, ns, name string) string {
	// pending-install is not self-healed. Recovery means uninstall-then-install,
	// which re-runs pre-install hooks — and a chart's migration hook is not
	// guaranteed to be re-entrant, so an automatic retry could apply a database
	// migration twice. That call belongs to an operator, not to a timeout.
	if rel.Info.Status == release.StatusPendingInstall {
		return fmt.Sprintf(
			"release %s/%s stuck in pending-install since %s. Helm refuses both install and upgrade "+
				"in this state. Recovery requires `helm uninstall %s -n %s` before retrying, which "+
				"re-runs pre-install hooks — verify they are safe to repeat first",
			ns, name, rel.Info.LastDeployed, name, ns)
	}
	return fmt.Sprintf(
		"release %s/%s stuck in %s since %s. Recover with `helm rollback %s -n %s`",
		ns, name, rel.Info.Status, rel.Info.LastDeployed, name, ns)
}

// manifestComplete reports whether every object the release renders actually
// exists and is ready.
//
// Stricter than manifestReady, and deliberately so, because it answers a
// different question. manifestReady asks "is the release up yet", which is a
// question about workloads, and Helm's ReadyChecker returns true for every kind
// it has no readiness notion for — a ConfigMap is "ready" whether or not it was
// ever created. That is right for polling an install this process is watching,
// and wrong for deciding whether an abandoned install finished: a release
// missing objects would be recorded as deployed and then never reconciled,
// because there is no drift detection inside a release.
//
// So existence is checked first, against the cluster, one object at a time.
func manifestComplete(
	ctx context.Context,
	conf *action.Configuration,
	client *transport.Client,
	rel *release.Release,
) (bool, string, error) {
	resources, err := conf.KubeClient.Build(strings.NewReader(rel.Manifest), false)
	if err != nil {
		return false, fmt.Sprintf("building release manifest: %v", err), nil
	}

	for _, res := range resources {
		if err := res.Get(); err != nil {
			return false, fmt.Sprintf("%s/%s is absent: %v",
				res.Mapping.GroupVersionKind.Kind, res.Name, err), nil
		}
	}

	return manifestReady(ctx, conf, client, rel)
}

// manifestReady checks every object the release rendered for readiness, reusing
// Helm's own ReadyChecker rather than reimplementing per-kind conditions.
func manifestReady(
	ctx context.Context,
	conf *action.Configuration,
	client *transport.Client,
	rel *release.Release,
) (bool, string, error) {
	resources, err := conf.KubeClient.Build(strings.NewReader(rel.Manifest), false)
	if err != nil {
		// A manifest object whose kind is not yet served (a CRD installed
		// earlier in this same release) is a normal transient state, not a
		// failure.
		return false, fmt.Sprintf("building release manifest: %v", err), nil
	}

	checker := kube.NewReadyChecker(client.Clientset, nil, kube.PausedAsReady(true))
	for _, res := range resources {
		ok, err := checker.IsReady(ctx, res)
		if err != nil {
			return false, fmt.Sprintf("%s/%s: %v", res.Mapping.GroupVersionKind.Kind, res.Name, err), nil
		}
		if !ok {
			return false, fmt.Sprintf("waiting for %s/%s", res.Mapping.GroupVersionKind.Kind, res.Name), nil
		}
	}
	return true, "", nil
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func (r *Release) List(ctx context.Context, request *resource.ListRequest) (*resource.ListResult, error) {
	ns, err := prov.ResolveListNamespace(request.AdditionalProperties, ResourceTypeRelease)
	if err != nil {
		return nil, err
	}

	conf, err := newActionConfig(r.Config, ns)
	if err != nil {
		return nil, err
	}

	list := action.NewList(conf)
	list.StateMask = action.ListAll

	releases, err := list.Run()
	if err != nil {
		return nil, fmt.Errorf("list helm releases in %s: %w", ns, err)
	}

	nativeIDs := make([]string, 0, len(releases))
	for _, rel := range releases {
		nativeIDs = append(nativeIDs, prov.NativeID(rel.Namespace, rel.Name))
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func decodeProperties(raw json.RawMessage) (*releaseProperties, error) {
	var props releaseProperties
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, fmt.Errorf("unmarshal %s properties: %w", ResourceTypeRelease, err)
	}
	return &props, nil
}

// lastRelease returns the most recent revision, or (nil, nil) when the release
// does not exist. Not-found is an expected outcome, not an error.
func lastRelease(conf *action.Configuration, name string) (*release.Release, error) {
	rel, err := conf.Releases.Last(name)
	if err != nil {
		if isReleaseNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read helm release %q: %w", name, err)
	}
	return rel, nil
}

func isReleaseNotFound(err error) bool {
	return goerrors.Is(err, driver.ErrReleaseNotFound) ||
		goerrors.Is(err, driver.ErrNoDeployedReleases) ||
		strings.Contains(err.Error(), "release: not found")
}

// validateChartRef rejects a chart reference Helm cannot resolve, before handing
// it to LocateChart.
//
// A bare name like "kratos" is unresolvable without somewhere to fetch it from,
// and Helm's own complaint ("non-absolute URLs should be in form of
// repo_name/path_to_chart") does not say what to do about it. This case is
// reached most often by adopting a release installed from an HTTP repo: Helm's
// release record does not retain the repository it came from, so Read cannot
// reconstruct repoURL and an extracted forma arrives with the bare name alone.
// resolveChartRef turns the forma's (chart, repoURL) pair into the arguments Helm
// actually wants: a chart reference, and the RepoURL to resolve it against.
//
// The split exists so `chart` is always the bare chart name — which is precisely
// what Helm records in the release, so it round-trips. Put the whole
// `oci://host/path/name` in `chart` instead and Read can only ever answer "name",
// which used to make every plain apply after the first fail as drift.
//
// The two repository kinds need opposite handling, which is why this is not just
// string concatenation:
//
//   - An HTTP repo has an index. Helm resolves a bare name against it, so the URL
//     goes to RepoURL and the chart reference stays the bare name.
//   - An OCI registry has no index. `helm pull nginx --repo oci://…` fails with
//     "invalid reference"; the full reference is mandatory. So the registry prefix
//     is joined onto the name here and RepoURL is left empty.
func resolveChartRef(chartRef, repoURL string) (ref, repo string) {
	if strings.HasPrefix(repoURL, ociScheme) {
		return strings.TrimSuffix(repoURL, "/") + "/" + chartRef, ""
	}
	return chartRef, repoURL
}

const ociScheme = "oci://"

// validateChartRef rejects a (chart, repoURL) pair Helm cannot resolve, before
// handing it to LocateChart.
func validateChartRef(chartRef, repoURL string) error {
	// A full OCI reference in `chart` works on first apply and then reports the
	// bare name back forever, so it is refused in favour of the split form. This
	// is a correctness guard, not style.
	if strings.HasPrefix(chartRef, ociScheme) {
		name := chartRef[strings.LastIndex(chartRef, "/")+1:]
		return fmt.Errorf(
			"%s: put the registry in repoURL and the chart name in chart, not a full "+
				"OCI reference in chart. Use repoURL = %q with chart = %q instead of "+
				"chart = %q. Helm records only the chart name, so the full reference "+
				"cannot be read back and every apply after the first would report drift",
			ResourceTypeRelease,
			strings.TrimSuffix(chartRef, "/"+name), name, chartRef)
	}

	// A bare name with nowhere to fetch it from. Helm's own complaint
	// ("non-absolute URLs should be in form of repo_name/path_to_chart") does not
	// say what to do about it. Reached most often by adopting a release installed
	// from a repo: Helm does not record which repository a release came from, so
	// Read cannot reconstruct repoURL.
	if repoURL != "" || strings.Contains(chartRef, "/") {
		return nil
	}
	return fmt.Errorf(
		"%s: chart %q is a bare name with no repoURL, which Helm cannot resolve. "+
			"Set repoURL to the chart repository — `https://…` for a classic repo or "+
			"`oci://host/path` for a registry — or give chart a local path. Adopting a "+
			"release installed from a repo always needs this: Helm does not record which "+
			"repository a release came from, so it cannot be discovered",
		ResourceTypeRelease, chartRef)
}

func loadChart(conf *action.Configuration, props *releaseProperties) (*chart.Chart, error) {
	if props.Chart == "" {
		return nil, fmt.Errorf("%s: chart is required", ResourceTypeRelease)
	}
	if err := validateChartRef(props.Chart, props.RepoURL); err != nil {
		return nil, err
	}

	// Only locates and caches the chart; carries no cluster auth.
	settings := cli.New()

	// Built via NewInstall rather than a bare ChartPathOptions because the
	// constructor copies cfg.RegistryClient into it — without that, any oci://
	// chart reference fails to resolve.
	inst := action.NewInstall(conf)
	inst.Version = props.Version

	// Either resolves a bare name against a repo index, or joins an OCI registry
	// prefix onto the name — see resolveChartRef. Either way the agent host needs
	// no `helm repo add`.
	ref, repo := resolveChartRef(props.Chart, props.RepoURL)
	inst.RepoURL = repo

	path, err := inst.LocateChart(ref, settings)
	if err != nil {
		return nil, fmt.Errorf("locate chart %q: %w", ref, err)
	}
	chrt, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart %q: %w", ref, err)
	}
	return chrt, nil
}

func inProgress(nativeID, reqID string, props json.RawMessage, msg string) *resource.StatusResult {
	return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
		Operation:          resource.OperationCheckStatus,
		OperationStatus:    resource.OperationStatusInProgress,
		RequestID:          reqID,
		NativeID:           nativeID,
		ResourceProperties: props,
		StatusMessage:      msg,
	}}
}

func failure(nativeID string, code resource.OperationErrorCode, msg string) *resource.StatusResult {
	return &resource.StatusResult{ProgressResult: &resource.ProgressResult{
		Operation:       resource.OperationCheckStatus,
		OperationStatus: resource.OperationStatusFailure,
		NativeID:        nativeID,
		ErrorCode:       code,
		StatusMessage:   msg,
	}}
}
