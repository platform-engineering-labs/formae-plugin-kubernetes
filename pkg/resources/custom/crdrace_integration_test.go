//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/custom"
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/helm"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const releaseType = "K8S::Helm::Release"

// uninstallAndWait tears a release down synchronously. Delete only *starts* the
// uninstall — it returns a RequestID and Helm keeps working — so a cleanup that
// fires and forgets exits the test process mid-uninstall and leaves the chart's
// cluster-scoped objects (RBAC, webhooks) behind to break the next run.
func uninstallAndWait(t *testing.T, rel prov.Provisioner, nativeID string) {
	t.Helper()
	del, err := rel.Delete(context.Background(), &resource.DeleteRequest{ResourceType: releaseType, NativeID: nativeID})
	if err != nil {
		t.Errorf("delete release %s: %v", nativeID, err)
		return
	}
	final := testutil.WaitForStatus(t, rel, nativeID, releaseType, del.ProgressResult.RequestID, 5*time.Minute)
	if final.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("uninstall of %s ended %s: %s", nativeID,
			final.ProgressResult.OperationStatus, final.ProgressResult.StatusMessage)
	}
}

func gizmoManifest(t *testing.T, ns string) json.RawMessage {
	return testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Gizmo",
		"metadata":   map[string]any{"name": "gizmo-1", "namespace": ns},
		"spec":       map[string]any{"size": 1},
	})
}

// TestHelmCRDThenCustomResourceSameApply is PLA-711: a Helm Release brings the
// CRD, and a K8S::Custom::Resource of that kind is applied in the same
// reconcile, with no dependency edge between them. The CR's Create must sit in
// its establish-retry loop until the kind is servable rather than failing the
// apply with "no matches for kind".
//
// The chart ships its CRD in crds/, which is the worst case: Helm applies those
// outside the release manifest, so the Release's readiness check never waits on
// them and the CR really can start before the kind exists.
func TestHelmCRDThenCustomResourceSameApply(t *testing.T) {
	env := testutil.SetupEnv(t)
	ctx := context.Background()

	chart, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "charts", "crdded"))
	if err != nil {
		t.Fatal(err)
	}

	rel := env.NewProvisioner(releaseType)
	cr := env.NewProvisioner(customType)

	crdID := prov.CustomResourceID("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "gizmos.example.com")
	wantID := prov.CustomResourceID("example.com/v1", "Gizmo", env.Namespace, "gizmo-1")
	t.Cleanup(func() {
		_, _ = cr.Delete(context.Background(), &resource.DeleteRequest{ResourceType: customType, NativeID: wantID})
		// crds/ CRDs outlive `helm uninstall`, so remove this one by hand.
		_, _ = cr.Delete(context.Background(), &resource.DeleteRequest{ResourceType: customType, NativeID: crdID})
	})

	// --- The knob bounds the wait, on the live path -------------------------
	// Proves the retry loop reads the plugin setting rather than a constant:
	// one second is not enough for any CRD, so this Create must give up.
	if err := config.SetSettings([]byte(`{"crdEstablishTimeoutSeconds":1}`)); err != nil {
		t.Fatal(err)
	}
	impatient := time.Now()
	if _, err := cr.Create(ctx, &resource.CreateRequest{ResourceType: customType, Properties: gizmoManifest(t, env.Namespace)}); err == nil {
		t.Fatal("Gizmo Create succeeded with no CRD installed")
	}
	if waited := time.Since(impatient); waited > 15*time.Second {
		t.Errorf("crdEstablishTimeoutSeconds=1 waited %s; the setting is being ignored", waited)
	}
	if err := config.SetSettings([]byte(`{"crdEstablishTimeoutSeconds":0}`)); err != nil {
		t.Fatal(err)
	}

	// --- The CR goes first, while its kind does not exist at all ------------
	// A single Create call with no retry loop on the test side: converging is
	// the whole fix. Ordering it ahead of the Release is what makes the race
	// deterministic — formae applies these two concurrently, and if the CR wins
	// the toss it must wait, not fail the apply.
	type createResult struct {
		res *resource.CreateResult
		err error
	}
	done := make(chan createResult, 1)
	start := time.Now()
	go func() {
		res, err := cr.Create(ctx, &resource.CreateRequest{ResourceType: customType, Properties: gizmoManifest(t, env.Namespace)})
		done <- createResult{res, err}
	}()

	// Long enough that the Gizmo Create is provably in its establish-retry
	// loop (backoff is 500ms) before the CRD exists.
	select {
	case got := <-done:
		t.Fatalf("Gizmo Create returned in %s, before the CRD was installed: err=%v", time.Since(start), got.err)
	case <-time.After(3 * time.Second):
	}

	// --- Now the release brings the CRD -------------------------------------
	relProps := testutil.MustMarshalJSON(t, map[string]any{
		"metadata": map[string]any{"name": "crdded", "namespace": env.Namespace},
		"chart":    chart,
	})
	created, err := rel.Create(ctx, &resource.CreateRequest{ResourceType: releaseType, Properties: relProps})
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	relReqID := created.ProgressResult.RequestID
	relID := prov.NativeID(env.Namespace, "crdded")
	t.Cleanup(func() { uninstallAndWait(t, rel, relID) })

	// --- The CR converges once the kind is servable -------------------------
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Gizmo never converged on the CRD the release installed: %v", got.err)
		}
		testutil.RequireSuccess(t, got.res.ProgressResult, "create Gizmo")
		if got.res.ProgressResult.NativeID != wantID {
			t.Errorf("Gizmo NativeID = %q, want %q", got.res.ProgressResult.NativeID, wantID)
		}
		t.Logf("Gizmo converged after %s of waiting on the CRD", time.Since(start))
	case <-time.After(2 * time.Minute):
		t.Fatal("Gizmo Create still hanging 2m after the CRD was installed")
	}

	// --- And the release still finishes -------------------------------------
	final := testutil.WaitForStatus(t, rel, "", releaseType, relReqID, 5*time.Minute)
	testutil.RequireSuccess(t, final.ProgressResult, "release install")
}
