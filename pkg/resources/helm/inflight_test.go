//go:build unit

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
)

// clearFlights drops all entries without cancelling. Production code removes
// its own flight when the operation finishes, so this exists only to keep one
// test's registrations out of the next test.
func clearFlights() {
	flightMu.Lock()
	flights = map[string]*inflight{}
	flightMu.Unlock()
}

// testTarget builds a config for a named kube context, which is what CacheKey
// hashes and therefore what separates one cluster from another.
func testTarget(t *testing.T, kubeContext string) *config.Config {
	t.Helper()
	cfg, err := config.FromTargetConfig([]byte(
		`{"Auth":{"Type":"Kubeconfig","Context":"` + kubeContext + `"}}`))
	if err != nil {
		t.Fatalf("FromTargetConfig(%q): %v", kubeContext, err)
	}
	return cfg
}

func TestFlightRegistry_RoundTrip(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	if got := lookupFlight(target, "ns", "app"); got != nil {
		t.Fatalf("lookup on an empty registry returned %+v", got)
	}

	registerFlight(target, "ns", "app", inflight{op: opInstall, revision: 1, fingerprint: "abc"}, func() {})

	got := lookupFlight(target, "ns", "app")
	if got == nil {
		t.Fatal("registered flight not found")
	}
	if got.op != opInstall || got.revision != 1 || got.fingerprint != "abc" {
		t.Errorf("flight = %+v, want install/1/abc", got)
	}

	removeFlight(target, "ns", "app")
	if got := lookupFlight(target, "ns", "app"); got != nil {
		t.Errorf("flight still present after remove: %+v", got)
	}
}

// The registry is keyed per release, so two releases in the same namespace must
// not evict one another.
func TestFlightRegistry_KeyedByNamespaceAndName(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	registerFlight(target, "ns", "a", inflight{revision: 1}, func() {})
	registerFlight(target, "ns", "b", inflight{revision: 2}, func() {})

	if f := lookupFlight(target, "ns", "a"); f == nil || f.revision != 1 {
		t.Errorf("ns/a = %+v, want revision 1", f)
	}
	if f := lookupFlight(target, "ns", "b"); f == nil || f.revision != 2 {
		t.Errorf("ns/b = %+v, want revision 2", f)
	}
}

// One agent drives many clusters, and the same namespace/name is a completely
// different release on each. Without the target in the key, an install on
// staging would be seen as the in-flight operation of the identically named
// release on production — and rejoined, reporting progress against work that
// was never started there.
func TestFlightRegistry_SeparatesTargets(t *testing.T) {
	defer clearFlights()
	staging := testTarget(t, "staging")
	production := testTarget(t, "production")

	registerFlight(staging, "prod", "api", inflight{revision: 1, fingerprint: "staging"}, func() {})

	if f := lookupFlight(production, "prod", "api"); f != nil {
		t.Fatalf("an install on staging was visible on production: %+v", f)
	}

	registerFlight(production, "prod", "api", inflight{revision: 7, fingerprint: "production"}, func() {})

	if f := lookupFlight(staging, "prod", "api"); f == nil || f.fingerprint != "staging" {
		t.Errorf("staging flight = %+v, want its own entry back", f)
	}
	if f := lookupFlight(production, "prod", "api"); f == nil || f.fingerprint != "production" {
		t.Errorf("production flight = %+v, want its own entry back", f)
	}

	// Removing one target's flight must leave the other's alone.
	removeFlight(staging, "prod", "api")
	if f := lookupFlight(production, "prod", "api"); f == nil {
		t.Error("removing the staging flight also removed production's")
	}
}

// Two targets that resolve to the same cluster legitimately share a key — the
// same reasoning the inventory cache already applies.
func TestFlightRegistry_SharesAKeyForTheSameCluster(t *testing.T) {
	defer clearFlights()

	registerFlight(testTarget(t, "same"), "ns", "app", inflight{revision: 3}, func() {})
	if f := lookupFlight(testTarget(t, "same"), "ns", "app"); f == nil || f.revision != 3 {
		t.Errorf("flight = %+v, want revision 3 from an equivalent target config", f)
	}
}

// A config we cannot identify a target from must miss rather than collide with
// everything, degrading to the behaviour there was before the registry existed.
func TestFlightRegistry_UnidentifiableTargetNeverMatches(t *testing.T) {
	defer clearFlights()

	registerFlight(nil, "ns", "app", inflight{revision: 1}, func() {})
	if f := lookupFlight(nil, "ns", "app"); f != nil {
		t.Errorf("a flight was registered against an unidentifiable target: %+v", f)
	}
	removeFlight(nil, "ns", "app")
}

// removeFlight must be safe to call for a key that was never registered, so a
// failure path that unwinds twice cannot corrupt the registry.
func TestFlightRegistry_RemoveIsIdempotent(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	registerFlight(target, "ns", "app", inflight{}, func() {})
	removeFlight(target, "ns", "app")
	removeFlight(target, "ns", "app")
	removeFlight(target, "ns", "never-registered")
}

func TestFlightRegistry_DrainCancelsEveryFlightOnce(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	var calls int32
	for _, name := range []string{"a", "b", "c"} {
		registerFlight(target, "ns", name, inflight{}, func() { atomic.AddInt32(&calls, 1) })
	}

	// Nothing calls removeFlight, so the drain must give up on its deadline
	// rather than block shutdown forever.
	if DrainInFlight(50 * time.Millisecond) {
		t.Error("drain reported success while flights were still registered")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("cancel called %d times, want 3", got)
	}

	// A second drain must not cancel the same flights again.
	DrainInFlight(10 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("cancel called %d times after a second drain, want 3", got)
	}
}

// The whole point of the drain: cancelled work removes itself, and the drain
// then returns before its deadline so shutdown is not delayed.
func TestFlightRegistry_DrainReturnsWhenFlightsFinish(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	registerFlight(target, "ns", "app", inflight{}, func() {
		go removeFlight(target, "ns", "app")
	})

	if !DrainInFlight(2 * time.Second) {
		t.Error("drain timed out although the flight removed itself")
	}
}

// Shutdown stops the whole process, so the drain has to reach every target, not
// just the one that happened to be looked up last.
func TestFlightRegistry_DrainSpansEveryTarget(t *testing.T) {
	defer clearFlights()

	var calls int32
	for _, ctx := range []string{"staging", "production"} {
		registerFlight(testTarget(t, ctx), "prod", "api", inflight{}, func() {
			atomic.AddInt32(&calls, 1)
		})
	}

	DrainInFlight(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("cancel called %d times, want one per target", got)
	}
}

func TestFlightRegistry_ConcurrentAccess(t *testing.T) {
	defer clearFlights()
	target := testTarget(t, "cluster-a")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a' + i%5))
			registerFlight(target, "ns", name, inflight{revision: i}, func() {})
			lookupFlight(target, "ns", name)
			removeFlight(target, "ns", name)
		}(i)
	}
	wg.Wait()
}

func TestFingerprint_IgnoresProviderComputedFields(t *testing.T) {
	base := &releaseProperties{
		Metadata: releaseMetadata{Name: "app", Namespace: "ns"},
		Chart:    "podinfo",
		Version:  "6.7.1",
		Values:   map[string]any{"replicaCount": 2.0},
	}
	// What Read hands back carries revision/status/appVersion/resourceNames.
	// Those describe the live release, not the request, so a re-driven Create
	// must fingerprint identically with or without them.
	withComputed := *base
	withComputed.Revision = 3
	withComputed.Status = "deployed"
	withComputed.AppVersion = "6.7.1"
	withComputed.ResourceNames = map[string][]string{"v1/Service": {"ns/app"}}

	if fingerprint(base) != fingerprint(&withComputed) {
		t.Error("computed fields changed the fingerprint")
	}
}

func TestFingerprint_ChangesWithDesiredState(t *testing.T) {
	base := &releaseProperties{
		Metadata: releaseMetadata{Name: "app", Namespace: "ns"},
		Chart:    "podinfo",
		Version:  "6.7.1",
		Values:   map[string]any{"replicaCount": 2.0},
	}

	for name, mutate := range map[string]func(*releaseProperties){
		"version":        func(p *releaseProperties) { p.Version = "6.7.2" },
		"values":         func(p *releaseProperties) { p.Values = map[string]any{"replicaCount": 3.0} },
		"chart":          func(p *releaseProperties) { p.Chart = "other" },
		"atomic":         func(p *releaseProperties) { p.Atomic = true },
		"timeoutSeconds": func(p *releaseProperties) { p.TimeoutSeconds = 900 },
	} {
		changed := *base
		changed.Values = map[string]any{"replicaCount": 2.0}
		mutate(&changed)
		if fingerprint(base) == fingerprint(&changed) {
			t.Errorf("%s: fingerprint unchanged after the desired state changed", name)
		}
	}
}

// Map iteration order must not leak into the hash, or a re-driven Create would
// fail to rejoin its own in-flight operation at random.
func TestFingerprint_StableAcrossMapOrdering(t *testing.T) {
	values := func() map[string]any {
		return map[string]any{"a": 1.0, "b": 2.0, "c": map[string]any{"d": 3.0, "e": 4.0}}
	}
	first := fingerprint(&releaseProperties{Values: values()})
	for i := 0; i < 20; i++ {
		if got := fingerprint(&releaseProperties{Values: values()}); got != first {
			t.Fatalf("fingerprint varied across runs: %s != %s", got, first)
		}
	}
}
