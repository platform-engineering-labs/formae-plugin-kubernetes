// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package helm

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestInventoryColdCacheConcurrent reproduces what discovery does to the
// inventory cache on a cold start.
//
// formae calls List once per resource type, concurrently. cachedInventory checks
// the cache under invMu but releases it before calling buildInventory, so on a
// cold cache every caller builds its own copy — and each build lists every Helm
// release in the cluster and gunzips its manifest. With enough callers that herd
// is slow enough that some fail, and collapseHelmOwned turns any failure into a
// silently unfiltered list: every object the chart rendered then surfaces as an
// unmanaged resource.
//
// Needs a cluster with at least one Helm release installed; the more objects it
// renders, the sharper the effect.
func TestInventoryColdCacheConcurrent(t *testing.T) {
	_, cfg := newTestRelease(t)

	// Cold, as after an agent restart.
	invalidateInventory(cfg)

	const callers = 21 // the profile's resource-type count

	var wg sync.WaitGroup
	errs := make([]error, callers)
	lens := make([]int, callers)
	start := make(chan struct{})

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, as discovery does
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			inv, err := InventoryFor(ctx, cfg)
			errs[i] = err
			if inv != nil {
				lens[i] = inv.Len()
			}
		}(i)
	}

	t0 := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(t0)

	var failed int
	var empty int
	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			failed++
			t.Logf("caller %2d: error %v", i, errs[i])
			continue
		}
		if lens[i] == 0 {
			empty++
		}
	}

	t.Logf("%d concurrent cold-cache callers in %s: %d errored, %d returned an empty inventory",
		callers, elapsed, failed, empty)

	// Every caller must get a usable inventory. One that errors or comes back
	// empty makes collapseHelmOwned pass the list through untouched, so the
	// chart's objects leak into discovery with nothing logged.
	if failed > 0 {
		t.Errorf("%d/%d cold-cache callers failed to build the inventory; "+
			"collapseHelmOwned turns each one into a silently unfiltered list",
			failed, callers)
	}
	if empty > 0 {
		t.Errorf("%d/%d cold-cache callers got an empty inventory", empty, callers)
	}
}

// TestInventorySingleFlight asserts the cold-cache herd is collapsed to one
// build rather than one per caller.
//
// Measured by elapsed time against a single warm build: with no single-flight,
// N concurrent cold callers each pay the full cost, so the wall clock lands far
// above one build's. This is the property that keeps the cache load-bearing
// instead of merely present.
func TestInventorySingleFlight(t *testing.T) {
	_, cfg := newTestRelease(t)

	invalidateInventory(cfg)
	t0 := time.Now()
	if _, err := InventoryFor(context.Background(), cfg); err != nil {
		t.Fatalf("warm-up build: %v", err)
	}
	oneBuild := time.Since(t0)
	t.Logf("a single build takes %s", oneBuild)

	invalidateInventory(cfg)

	const callers = 21
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = InventoryFor(context.Background(), cfg)
		}()
	}
	t1 := time.Now()
	close(start)
	wg.Wait()
	herd := time.Since(t1)

	t.Logf("%d concurrent cold callers took %s (one build: %s)", callers, herd, oneBuild)

	// Generous bound: single-flight should make this ~one build. Three times a
	// single build still fails a per-caller herd by a wide margin on any chart
	// big enough to matter, without flaking on a fast one.
	if limit := 3 * oneBuild; herd > limit && herd > 2*time.Second {
		t.Errorf("cold-cache herd took %s, over the %s bound: every caller is "+
			"building its own inventory instead of sharing one", herd, limit)
	}
}
