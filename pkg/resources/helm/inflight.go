// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
)

// The in-flight registry tracks the Helm operations this plugin process is
// running right now.
//
// It exists because the agent does not resume an interrupted operation, it
// re-drives it: ReRunIncompleteCommands resets an InProgress resource update to
// NotStarted and calls Create again from scratch, discarding the RequestID
// (formae internal/metastructure/metastructure.go:1262). The plugin is a
// separate process, so when only the agent restarts our install goroutine is
// still running — and without this registry the re-driven Create sees Helm's
// pending lock, reports ResourceConflict until it runs out of attempts, and
// fails a command whose install then goes on to succeed.
//
// Two hard rules, because the SDK requires plugins to "remain stateless to
// ensure non-flaky restarts and hot reloads" (pkg/plugin/resource.go:22):
//
//  1. The cluster record stays the source of truth. This map is consulted only
//     to recognise work THIS process is doing, and an empty map must degrade to
//     exactly the behaviour we had before it existed.
//  2. A miss proves only that this process is not running the operation. Helm
//     CLI and another agent may still exist. Completed worker errors are kept
//     briefly so failures before the first cluster record remain reportable.
type inflight struct {
	op        opKind
	requestOp opKind
	revision  int
	// fingerprint identifies the desired state that was submitted, so a
	// re-driven call can tell "my own operation" from "somebody else's
	// operation that happens to hold the lock".
	fingerprint     string
	authFingerprint string
	bindingID       string
	generation      string
	bridge          *authBridge
	preparing       bool
	stopping        bool
	finished        bool
	statusReaders   int // matching Status and rejoin callbacks retaining this generation
	reported        bool
	outcome         error
	finishedAt      time.Time
	started         time.Time
	deadline        time.Time
	cancel          func()
}

var (
	flightMu     sync.Mutex
	flights      = map[string]*inflight{}
	activeDrains int
)

// flightKey scopes a release to the live authenticated kube-system UID.
func flightKey(scope, namespace, name string) (string, bool) {
	if scope == "" {
		return "", false
	}
	return scope + "|" + prov.NativeID(namespace, name), true
}

// reserveRejoinFlight atomically reserves a new flight or retains the matching
// existing outcome. Callers release a matched reader on callback exit, so a
// concurrent terminal Status cannot remove it during credential service.
func reserveRejoinFlight(scope, namespace, name string, f inflight) (*inflight, bool) {
	return reserveFlightMatching(scope, namespace, name, f, func(existing *inflight) bool {
		return matchesRejoin(existing, &f)
	})
}

func matchesRejoin(existing, wanted *inflight) bool {
	if existing == nil || existing.preparing || existing.authFingerprint != wanted.authFingerprint || existing.bindingID != wanted.bindingID {
		return false
	}
	if wanted.op == opDelete {
		return existing.op == opDelete
	}
	return existing.requestOp == wanted.requestOp && existing.fingerprint == wanted.fingerprint
}

func reserveFlightMatching(scope, namespace, name string, f inflight, retain func(*inflight) bool) (*inflight, bool) {
	key, ok := flightKey(scope, namespace, name)
	if !ok {
		return nil, false
	}
	if f.started.IsZero() {
		f.started = time.Now()
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if activeDrains > 0 {
		return nil, false
	}
	if existing := flights[key]; existing != nil {
		if retain != nil && retain(existing) {
			existing.statusReaders++
		}
		cp := *existing
		return &cp, false
	}
	flights[key] = &f
	cp := f
	return &cp, true
}

// lookupFlight returns the operation this process is running for the release on
// this target, or nil. The returned copy is safe to read without the lock.
func lookupFlight(scope, namespace, name string) *inflight {
	key, ok := flightKey(scope, namespace, name)
	if !ok {
		return nil
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	f, present := flights[key]
	if !present {
		return nil
	}
	cp := *f
	return &cp
}

// removeOwnedFlight fences late completion against a later flight generation.
func removeOwnedFlight(scope, namespace, name, generation string) {
	key, ok := flightKey(scope, namespace, name)
	if !ok {
		return
	}
	flightMu.Lock()
	defer flightMu.Unlock()
	if f := flights[key]; f != nil && f.generation == generation {
		f.reported = true
		if f.statusReaders == 0 {
			delete(flights, key)
		}
	}
}

// DrainInFlight cancels every in-flight operation and waits for them to
// deregister. Reports whether they all finished within the timeout.
//
// This is what turns a graceful stop into a recoverable one. Cancelling makes
// Helm run failRelease (install.go:411, upgrade.go:399), which sets the release
// to `failed` — a state the next apply can upgrade over. Being killed without
// cancelling leaves it `pending-install`, which Helm refuses to install OR
// upgrade, and which only `helm uninstall` clears.
//
// Each flight is cancelled at most once, so calling this twice is harmless.
func DrainInFlight(timeout time.Duration) bool {
	flightMu.Lock()
	activeDrains++
	cancels := make([]func(), 0, len(flights))
	for _, f := range flights {
		if !f.finished {
			f.stopping = true
		}
		if f.cancel != nil {
			cancels = append(cancels, f.cancel)
			f.cancel = nil
		}
	}
	flightMu.Unlock()
	defer func() { flightMu.Lock(); activeDrains--; flightMu.Unlock() }()

	for _, cancel := range cancels {
		cancel()
	}

	// Polled rather than wait-grouped on purpose: Add/Done pairing across every
	// early-return path in submit is a bug waiting to happen, and a 20ms poll
	// during shutdown costs nothing.
	deadline := time.Now().Add(timeout)
	for {
		flightMu.Lock()
		remaining := 0
		for _, f := range flights {
			if !f.finished {
				remaining++
			}
		}
		flightMu.Unlock()
		if remaining == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fingerprint hashes the desired state a request carries.
//
// Only the input fields count. revision, status, appVersion and resourceNames
// describe the live release rather than the request, and Read populates them, so
// including them would stop a re-driven Create from matching its own in-flight
// operation.
func fingerprint(p *releaseProperties) string {
	if p == nil {
		return ""
	}
	inputs := *p
	inputs.Revision = 0
	inputs.Status = ""
	inputs.AppVersion = ""
	inputs.ResourceNames = nil

	// encoding/json sorts map keys, so this is canonical for nested values too.
	encoded, err := json.Marshal(&inputs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
