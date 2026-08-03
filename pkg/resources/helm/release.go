// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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

	Chart           string         `json:"chart"`
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
func (r *Release) submit(
	ctx context.Context,
	props *releaseProperties,
	withholdNativeID bool,
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

	// Helm treats pending as a pessimistic lock: both Install.availableName and
	// Upgrade.prepareUpgrade refuse to proceed. Report rather than fight it —
	// Status decides whether it is live or abandoned.
	if releaseIsPending(current) {
		return &resource.ProgressResult{
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        nativeIDUnless(withholdNativeID, ns, name),
			RequestID:       requestID(ns, name, current.Version, opForStatus(current.Info.Status)),
			StatusMessage: fmt.Sprintf(
				"release %s/%s is %s from an earlier operation; waiting for it to settle",
				ns, name, current.Info.Status),
		}, nil
	}

	chrt, err := loadChart(conf, props)
	if err != nil {
		return nil, err
	}

	// Detached: the request context is cancelled the moment submit returns.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), props.timeout())

	op := opInstall
	target := 1
	if current != nil {
		op = opUpgrade
		target = current.Version + 1
	}

	done := make(chan error, 1)
	go func() {
		defer cancel()
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
		NativeID:        nativeIDUnless(withholdNativeID, ns, name),
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
	inst.Labels = props.Metadata.Labels

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
	up.Labels = props.Metadata.Labels
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
			Labels:    rel.Labels,
		},
		Values:        rel.Config,
		Revision:      rel.Version,
		ResourceNames: resourceNames(rel),
	}
	if rel.Info != nil {
		props.Status = rel.Info.Status.String()
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		props.Chart = rel.Chart.Metadata.Name
		props.Version = rel.Chart.Metadata.Version
		props.AppVersion = rel.Chart.Metadata.AppVersion
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
	un.Wait = false
	un.Timeout = defaultTimeoutSeconds * time.Second
	// KeepHistory=false: formae's own state records the intent to delete, so a
	// retained uninstall record buys nothing and makes a later reinstall of the
	// same name need a replace strategy.
	un.KeepHistory = false

	if _, err := un.Run(name); err != nil {
		if k8serrors.IsNotFound(err) || isReleaseNotFound(err) {
			return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
			}}, nil
		}
		return nil, fmt.Errorf("uninstall release %s/%s: %w", ns, name, err)
	}
	invalidateInventory(r.Config)

	return &resource.DeleteResult{ProgressResult: &resource.ProgressResult{
		Operation:       resource.OperationDelete,
		OperationStatus: resource.OperationStatusSuccess,
		NativeID:        prov.NativeID(ns, name),
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

	if op == opDelete {
		return &resource.StatusResult{ProgressResult: deleteStatus(rel, ns, name)}, nil
	}
	return r.installStatus(ctx, conf, rel, ns, name, wantRevision, op, request.RequestID)
}

func deleteStatus(rel *release.Release, ns, name string) *resource.ProgressResult {
	if rel == nil {
		return &resource.ProgressResult{
			Operation:       resource.OperationCheckStatus,
			OperationStatus: resource.OperationStatusSuccess,
		}
	}
	return &resource.ProgressResult{
		Operation:       resource.OperationCheckStatus,
		OperationStatus: resource.OperationStatusInProgress,
		NativeID:        prov.NativeID(ns, name),
		StatusMessage:   fmt.Sprintf("release %s/%s still present (%s)", ns, name, rel.Info.Status),
	}
}

func (r *Release) installStatus(
	ctx context.Context,
	conf *action.Configuration,
	rel *release.Release,
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
		if stalled(rel) {
			return failure(pendingID, resource.OperationErrorCodeGeneralServiceException, stalledMessage(rel, ns, name)), nil
		}
		return inProgress(pendingID, reqID, nil, fmt.Sprintf("release is %s", rel.Info.Status)), nil

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
// This is a heuristic and it cannot be otherwise: Helm's pending status IS the
// lock, so nothing distinguishes "process died" from "a slow pre-install hook is
// still running". The window is therefore deliberately wide — healing early
// would mean acting against a live install.
func stalled(rel *release.Release) bool {
	if rel.Info == nil {
		return false
	}
	started := rel.Info.LastDeployed.Time
	if started.IsZero() {
		started = rel.Info.FirstDeployed.Time
	}
	if started.IsZero() {
		return false
	}
	return time.Since(started) > 2*defaultTimeoutSeconds*time.Second
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

func opForStatus(s release.Status) opKind {
	if s == release.StatusPendingInstall {
		return opInstall
	}
	return opUpgrade
}

func loadChart(conf *action.Configuration, props *releaseProperties) (*chart.Chart, error) {
	if props.Chart == "" {
		return nil, fmt.Errorf("%s: chart is required", ResourceTypeRelease)
	}

	// Only locates and caches the chart; carries no cluster auth.
	settings := cli.New()

	// Built via NewInstall rather than a bare ChartPathOptions because the
	// constructor copies cfg.RegistryClient into it — without that, any oci://
	// chart reference fails to resolve.
	inst := action.NewInstall(conf)
	inst.Version = props.Version
	// Resolves a bare chart name against the repo index directly, so the agent
	// host needs no `helm repo add`.
	inst.RepoURL = props.RepoURL

	path, err := inst.LocateChart(props.Chart, settings)
	if err != nil {
		return nil, fmt.Errorf("locate chart %q: %w", props.Chart, err)
	}
	chrt, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart %q: %w", props.Chart, err)
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
