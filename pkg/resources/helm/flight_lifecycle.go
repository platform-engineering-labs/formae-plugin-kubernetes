// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/auth"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae/pkg/credential"
	"github.com/platform-engineering-labs/formae/pkg/plugin"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func flightIdentity(ctx context.Context, cfg *config.Config) (string, string, error) {
	identity, err := cfg.AuthFingerprint()
	if err != nil {
		return "", "", err
	}
	if !cfg.UsesOidc() {
		return identity, "", nil
	}
	info, ok := plugin.OidcOperationMetadata(ctx)
	if !ok {
		return "", "", fmt.Errorf("helm OIDC requires trusted operation metadata")
	}
	return identity, info.BindingID, nil
}
func flightRequestID(ns, name string, f *inflight) string {
	return requestID(ns, name, f.revision, f.op) + "#" + f.generation
}
func requestGeneration(id string) (string, error) {
	_, gen, found := strings.Cut(id, "#")
	if !found {
		return "", nil
	}
	if _, err := uuid.Parse(gen); err != nil {
		return "", fmt.Errorf("malformed Helm flight generation")
	}
	return gen, nil
}
func flightConflict(ns, name string) *resource.ProgressResult {
	return &resource.ProgressResult{OperationStatus: resource.OperationStatusFailure, ErrorCode: resource.OperationErrorCodeResourceConflict, StatusMessage: fmt.Sprintf("release %s/%s belongs to a different or still preparing Helm operation", ns, name)}
}
func updateOwnedFlight(scope, ns, name, gen string, update func(*inflight)) {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if f := flights[key]; f != nil && f.generation == gen {
		update(f)
	}
}
func finishFlight(scope, ns, name, gen string, err error) {
	updateOwnedFlight(scope, ns, name, gen, func(f *inflight) {
		if f.finished {
			return
		}
		f.finished = true
		f.finishedAt = time.Now()
		f.outcome = err
		f.cancel = nil
		if f.bridge != nil {
			f.bridge.Close(err)
		}
	})
	// Retain undelivered outcomes until a matching poll consumes them. Cleanup
	// requests removal after five minutes; active bounded Status readers retain
	// ownership until they exit, so cleanup cannot race their recovery work.
	time.AfterFunc(5*time.Minute, func() { removeOwnedFlight(scope, ns, name, gen) })
}
func flightProgress(ns, name string, f *inflight, isCreate bool) *resource.ProgressResult {
	return &resource.ProgressResult{OperationStatus: resource.OperationStatusInProgress, NativeID: nativeIDUnless(isCreate, ns, name), RequestID: flightRequestID(ns, name, f), StatusMessage: "Helm operation running"}
}

// A release record can become terminal before Helm returns. Only the owning
// worker's immutable outcome permits a terminal storage result; the same lock
// both observes completion and consumes it, so a late finish cannot strand it.
func reconcileFlightResult(scope, ns, name, generation, reqID string, result *resource.StatusResult) *resource.StatusResult {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return result
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	f := flights[key]
	if f == nil || f.generation != generation {
		if result == nil {
			return &resource.StatusResult{ProgressResult: flightConflict(ns, name)}
		}
		return result
	}
	if f.finished && f.outcome != nil {
		f.reported = true
		if f.statusReaders == 0 {
			delete(flights, key)
		}
		return failure(nativeIDUnless(f.op == opInstall, ns, name), resource.OperationErrorCodeGeneralServiceException, fmt.Sprintf("Helm operation failed: %v", f.outcome))
	}
	if result == nil || result.ProgressResult == nil || result.ProgressResult.OperationStatus == resource.OperationStatusInProgress {
		return result
	}
	if !f.finished {
		return inProgress(nativeIDUnless(f.op == opInstall, ns, name), reqID, nil, "waiting for Helm worker completion")
	}
	f.reported = true
	if f.statusReaders == 0 {
		delete(flights, key)
	}
	return result
}

// Close on successful worker completion is a wakeup, not a failed operation.
// Re-read the owning generation after service: finishFlight closes under the
// registry lock, so a closed bridge cannot precede its published outcome.
// The caller must retain its matched flight until service and result observation
// finish; both Status and rejoin release that reader only on callback exit.
func serviceFlight(ctx context.Context, scope, ns, name string, f *inflight, source auth.TokenSource) (*inflight, error) {
	err := f.bridge.Service(ctx, source)
	fresh := lookupFlight(scope, ns, name)
	if fresh != nil && fresh.generation == f.generation && fresh.finished {
		return fresh, nil
	}
	return fresh, err
}

// Activation and drain share the registry lock. A reservation canceled during
// preparation can never publish a Background worker after the drain's scan.
func activateFlight(scope, ns, name, generation string, cancel func(), update func(*inflight)) (*inflight, bool) {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return nil, false
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	f := flights[key]
	if f == nil || f.generation != generation || f.stopping || activeDrains > 0 {
		return nil, false
	}
	update(f)
	f.cancel = cancel
	f.preparing = false
	cp := *f
	return &cp, true
}

// An error actually delivered by submit needs no future RequestID to report it.
// Keep other outcomes when the handler timed out/lost its response or the worker
// is still live; only consume the exact completed error being returned now.
func consumeSynchronousFailure(scope, ns, name, generation string, reported error) {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if f := flights[key]; f != nil && f.generation == generation && f.finished && f.outcome != nil && errors.Is(reported, f.outcome) {
		f.reported = true
		if f.statusReaders == 0 {
			delete(flights, key)
		}
	}
}

// A Status read may recover the record, so its ownership must outlive another
// matching poll's terminal report and the retention timer. No callback context
// is retained here; each caller releases its count with defer.
func acquireStatusFlight(scope, ns, name, requestedGeneration string, candidate inflight) (*inflight, bool) {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return nil, false
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if activeDrains > 0 {
		return nil, false
	}
	f := flights[key]
	if f == nil {
		flights[key] = &candidate
		cp := candidate
		return &cp, true
	}
	if !f.preparing && f.generation == requestedGeneration && f.op == candidate.op && f.revision == candidate.revision && f.authFingerprint == candidate.authFingerprint && f.bindingID == candidate.bindingID {
		f.statusReaders++
	}
	cp := *f
	return &cp, false
}

// releaseStatusFlight releases a reader retained by Status or matching rejoin.
func releaseStatusFlight(scope, ns, name, generation string) {
	key, ok := flightKey(scope, ns, name)
	if !ok {
		return
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if f := flights[key]; f != nil && f.generation == generation {
		if f.statusReaders > 0 {
			f.statusReaders--
		}
		if f.statusReaders == 0 && f.reported {
			delete(flights, key)
		}
	}
}

// Once activation has happened, a transient handler read or service failure
// cannot orphan its worker by making the SDK stop polling. Actual worker
// failures and the action deadline remain terminal.
func progressAfterFlightError(scope, ns, name string, owned *inflight, isCreate bool, err error) (*resource.ProgressResult, error) {
	current := lookupFlight(scope, ns, name)
	if current == nil || current.generation != owned.generation {
		return flightConflict(ns, name), nil
	}
	if current.finished && current.outcome != nil {
		// The worker outcome replaces the callback error and is returned below.
		consumeSynchronousFailure(scope, ns, name, current.generation, current.outcome)
		return nil, current.outcome
	}
	if !current.finished && !current.deadline.IsZero() && !time.Now().Before(current.deadline) {
		if current.cancel != nil {
			current.cancel()
		}
		return failure(nativeIDUnless(isCreate, ns, name), resource.OperationErrorCodeGeneralServiceException, "Helm action deadline exhausted").ProgressResult, nil
	}
	if isFlightServiceError(err) {
		return flightProgress(ns, name, current, isCreate), nil
	}
	return nil, err
}

// observeUnidentifiedFlight grants no physical-cluster authority. It scans only
// for the exact unpredictable generation and full immutable request/auth/binding
// identity to observe a pre-existing deadline/outcome. It never returns a scope,
// touches the bridge, cancels work, retains a reader, or consumes the outcome.
// Without this observation, UID outages could keep SDK InProgress polling alive
// forever: the host has a per-call watchdog, not a total operation deadline.
func observeUnidentifiedFlight(ns, name string, revision int, op opKind, generation, identity, binding, reqID string) *resource.StatusResult {
	flightMu.Lock()
	defer flightMu.Unlock()
	native := nativeIDUnless(op == opInstall, ns, name)
	if generation != "" && identity != "" {
		for key, f := range flights {
			if !strings.HasSuffix(key, "|"+ns+"/"+name) || f.preparing || f.generation != generation || f.op != op || f.revision != revision || f.authFingerprint != identity || f.bindingID != binding {
				continue
			}
			if f.finished && f.outcome != nil {
				return failure(native, resource.OperationErrorCodeGeneralServiceException, fmt.Sprintf("Helm operation failed: %v", f.outcome))
			}
			if f.deadline.IsZero() || !time.Now().Before(f.deadline) {
				return failure(native, resource.OperationErrorCodeGeneralServiceException, "Helm action deadline exhausted while cluster identity was unavailable; inspect the release before retrying")
			}
			return inProgress(native, reqID, nil, "temporary Helm service failure; retrying status within the retained action deadline")
		}
	}
	return failure(native, resource.OperationErrorCodeGeneralServiceException, "cluster identity unavailable and no matching bounded Helm generation retained; retry with a fresh authorized operation")
}

// The bounded receiver uses ErrInternal for its own deadline as well as
// protocol faults. Only an exact retained flight may tolerate that ambiguity;
// preparation, ordinary auth and unmatched/restarted operations fail closed.
func isFlightServiceError(err error) bool {
	return auth.IsTransient(err) || errors.Is(err, credential.ErrInternal)
}
