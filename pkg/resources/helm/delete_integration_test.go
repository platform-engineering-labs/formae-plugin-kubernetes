//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	deleteTestNamespace = "formae-helm-delete-it"
	deleteTestRelease   = "hooked-del"
)

// Delete must register its uninstall in the in-flight registry, because that
// registry is the only thing that distinguishes "formae is uninstalling this
// right now" from "an uninstall died half way". Without it the first Status poll
// — 20s after Delete by default — sees a release record still present with no
// operation behind it and reports the uninstall as abandoned, which asks the
// agent to re-drive Delete and start a second uninstall of the same release.
//
// Asserted on the registry rather than on the poll result, because registration
// is synchronous inside Delete while the race between purge and poll is not.
func TestDelete_RegistersItsUninstall(t *testing.T) {
	r, cfg := newTestRelease(t)
	ctx := context.Background()
	nsClient := r.Client.CoreV1().Namespaces()

	ensureNamespaceNamed(t, nsClient, deleteTestNamespace)
	t.Cleanup(func() {
		_ = nsClient.Delete(context.Background(), deleteTestNamespace, metav1.DeleteOptions{})
	})

	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": deleteTestRelease, "namespace": deleteTestNamespace},
		"chart":    chartPath(t),
		"values":   map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   raw,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	nativeID := prov.NativeID(deleteTestNamespace, deleteTestRelease)
	del, err := r.Delete(ctx, &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: ResourceTypeRelease,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	flight := lookupFlight(cfg, deleteTestNamespace, deleteTestRelease)
	if flight == nil {
		t.Fatal("Delete registered no in-flight operation: the next Status poll will " +
			"report this live uninstall as abandoned and re-drive Delete")
	}
	if flight.op != opDelete {
		t.Errorf("in-flight op = %q, want %q", flight.op, opDelete)
	}
	if flight.deadline.IsZero() {
		t.Error("in-flight deadline is zero: stalled() cannot bound this uninstall")
	}

	if final := pollUntilTerminal(t, r, nativeID, del.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("uninstall ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	// Completing must clear the registry, or a later abandoned uninstall of the
	// same release is suppressed forever.
	if flight := lookupFlight(cfg, deleteTestNamespace, deleteTestRelease); flight != nil {
		t.Errorf("in-flight entry survived the uninstall: %+v", flight)
	}
}
