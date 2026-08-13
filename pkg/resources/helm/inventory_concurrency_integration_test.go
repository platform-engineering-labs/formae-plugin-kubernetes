// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package helm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	inventoryNamespace = "formae-helm-inventory-it"
	inventoryRelease   = "inventory-herd"
)

// installInventoryFixture puts one release in the cluster so there is something
// to inventory, and takes it away again afterwards.
func installInventoryFixture(t *testing.T, r *Release) {
	t.Helper()

	nsClient := r.Client.CoreV1().Namespaces()
	ensureNamespaceNamed(t, nsClient, inventoryNamespace)
	t.Cleanup(func() {
		_ = nsClient.Delete(context.Background(), inventoryNamespace, metav1.DeleteOptions{})
	})

	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": inventoryRelease, "namespace": inventoryNamespace},
		"chart":    chartPath(t),
		"values":   map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := r.Create(context.Background(), &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   raw,
	})
	if err != nil {
		t.Fatalf("install inventory fixture: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("inventory fixture install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	nativeID := prov.NativeID(inventoryNamespace, inventoryRelease)
	t.Cleanup(func() {
		del, err := r.Delete(context.Background(), &resource.DeleteRequest{
			NativeID:     nativeID,
			ResourceType: ResourceTypeRelease,
		})
		if err != nil {
			t.Logf("cleanup delete %s: %v", nativeID, err)
			return
		}
		pollUntilTerminal(t, r, nativeID, del.ProgressResult.RequestID)
	})
}

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
// The release it needs is installed here rather than assumed. Depending on
// whatever the cluster happened to hold made this test pass on a developer's
// machine — where earlier runs leave releases behind — and fail on a fresh kind
// cluster, where every caller correctly returns an empty inventory because there
// is nothing to inventory.
func TestInventoryColdCacheConcurrent(t *testing.T) {
	r, cfg := newTestRelease(t)
	installInventoryFixture(t, r)

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
