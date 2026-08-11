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

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
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
//  2. It may only ever suppress a verdict, never accelerate one. A miss proves
//     that this process is not running the operation — not that nobody is. The
//     Helm CLI and a second agent are both allowed to exist.
type inflight struct {
	op       opKind
	revision int
	// fingerprint identifies the desired state that was submitted, so a
	// re-driven call can tell "my own operation" from "somebody else's
	// operation that happens to hold the lock".
	fingerprint string
	started     time.Time
	deadline    time.Time
	cancel      func()
}

var (
	flightMu sync.Mutex
	flights  = map[string]*inflight{}
)

// flightKey scopes a release to its target.
//
// Namespace and name alone are not unique: one agent drives many clusters, and
// `prod/api` on staging is a different release from `prod/api` on production.
// Keyed on those alone, an install on one cluster would be mistaken for the
// in-flight operation of another and rejoined — reporting progress against work
// that was never started there. cfg.CacheKey is the same target identity the
// inventory cache is keyed on.
//
// Reports false when the target cannot be identified, which makes every
// operation on it invisible to rejoin and to the drain. That is the safe
// direction: it degrades to the behaviour there was before this registry
// existed. In practice it cannot happen here, because newActionConfig has
// already parsed the same auth block by the time a flight is registered.
func flightKey(cfg *config.Config, namespace, name string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	target, err := cfg.CacheKey()
	if err != nil {
		return "", false
	}
	return target + "|" + prov.NativeID(namespace, name), true
}

// registerFlight records an operation as running in this process. Overwrites any
// existing entry for the release: Helm's own lock guarantees there is at most
// one real operation per release, so a leftover entry is stale by definition.
func registerFlight(cfg *config.Config, namespace, name string, f inflight, cancel func()) {
	key, ok := flightKey(cfg, namespace, name)
	if !ok {
		return
	}
	f.cancel = cancel
	if f.started.IsZero() {
		f.started = time.Now()
	}
	flightMu.Lock()
	flights[key] = &f
	flightMu.Unlock()
}

// lookupFlight returns the operation this process is running for the release on
// this target, or nil. The returned copy is safe to read without the lock.
func lookupFlight(cfg *config.Config, namespace, name string) *inflight {
	key, ok := flightKey(cfg, namespace, name)
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

// removeFlight forgets an operation. Safe to call for a key that is not present,
// so an unwinding failure path can call it unconditionally.
func removeFlight(cfg *config.Config, namespace, name string) {
	key, ok := flightKey(cfg, namespace, name)
	if !ok {
		return
	}
	flightMu.Lock()
	delete(flights, key)
	flightMu.Unlock()
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
	cancels := make([]func(), 0, len(flights))
	for _, f := range flights {
		if f.cancel != nil {
			cancels = append(cancels, f.cancel)
			f.cancel = nil
		}
	}
	flightMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	// Polled rather than wait-grouped on purpose: Add/Done pairing across every
	// early-return path in submit is a bug waiting to happen, and a 20ms poll
	// during shutdown costs nothing.
	deadline := time.Now().Add(timeout)
	for {
		flightMu.Lock()
		remaining := len(flights)
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
