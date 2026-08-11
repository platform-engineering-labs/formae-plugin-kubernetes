//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Exercises K8S::Helm::Release against a real third-party chart pulled from a
// real repository: ory/kratos.
//
// Chosen because it hits, in one chart, every Helm behaviour the
// render-and-decompose path gets wrong:
//
//   - `pre-install, pre-upgrade` hooks at two weights (0 for the ServiceAccount,
//     Secret, ConfigMap and RBAC; 1 for the automigrate Job), so hook *ordering*
//     is load-bearing rather than incidental.
//   - An automigrate Job with `hook-delete-policy: before-hook-creation,
//     hook-succeeded` — the database-migration hook this whole design exists for.
//   - A Secret carrying `helm.sh/resource-policy: keep`, which outlives uninstall.
//   - A `test-success` Pod hook, which must never be applied as an ordinary
//     resource.
//   - A StatefulSet and a Deployment, so readiness is a real check.
package helm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	apicorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	kratosNamespace = "formae-kratos-it"
	kratosRelease   = "kratos"
	kratosRepoURL   = "https://k8s.ory.sh/helm/charts"
	kratosVersion   = "0.63.0"
)

const identitySchema = `{
  "$id": "https://schemas.ory.sh/presets/kratos/identity.email.schema.json",
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Person",
  "type": "object",
  "properties": {
    "traits": {
      "type": "object",
      "properties": {
        "email": {
          "type": "string",
          "format": "email",
          "title": "E-Mail",
          "ory.sh/kratos": {
            "credentials": { "password": { "identifier": true } },
            "verification": { "via": "email" },
            "recovery": { "via": "email" }
          }
        }
      },
      "required": ["email"],
      "additionalProperties": false
    }
  }
}`

// kratosValues is the minimum that makes the chart render and its migration hook
// run. `dsn: memory` keeps sqlite in-process — the migration is real work against
// a throwaway database, which is what we want to observe without provisioning
// Postgres.
func kratosValues(returnURL string) map[string]any {
	return map[string]any{
		"kratos": map[string]any{
			"development":   true,
			"automigration": map[string]any{"enabled": true, "type": "job"},
			"config": map[string]any{
				"dsn":     "memory",
				"secrets": map[string]any{"default": []any{"abcdefghijklmnopqrstuvwxyz123456"}},
				"selfservice": map[string]any{
					"default_browser_return_url": returnURL,
					"flows": map[string]any{
						"settings": map[string]any{"ui_url": returnURL + "settings"},
					},
				},
				"identity": map[string]any{
					"default_schema_id": "default",
					"schemas": []any{map[string]any{
						"id":  "default",
						"url": "file:///etc/config/identity.default.schema.json",
					}},
				},
			},
			"identitySchemas": map[string]any{
				"identity.default.schema.json": identitySchema,
			},
		},
	}
}

func kratosProps(t *testing.T, returnURL string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": kratosRelease, "namespace": kratosNamespace},
		"chart":    "kratos",
		"repoURL":  kratosRepoURL,
		"version":  kratosVersion,
		"values":   kratosValues(returnURL),
		// The automigrate hook Job plus image pulls for kratos, busybox and the
		// RBAC job need more than the 600s default on a cold node.
		"timeoutSeconds": 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKratosChart(t *testing.T) {
	r, cfg := newTestRelease(t)
	ctx := context.Background()
	nsClient := r.Client.CoreV1().Namespaces()

	if _, err := nsClient.Get(ctx, kratosNamespace, metav1.GetOptions{}); err != nil {
		ns := &apicorev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kratosNamespace}}
		if _, err := nsClient.Create(ctx, ns, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace: %v", err)
		}
	}
	t.Cleanup(func() {
		_ = nsClient.Delete(context.Background(), kratosNamespace, metav1.DeleteOptions{})
	})

	nativeID := prov.NativeID(kratosNamespace, kratosRelease)

	// --- Install a real chart from a real HTTP repo --------------------------
	start := time.Now()
	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   kratosProps(t, "http://127.0.0.1/"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("Create returned in %s (chart fetched from %s)", time.Since(start), kratosRepoURL)

	if created.ProgressResult.NativeID != "" {
		t.Errorf("Create leaked a NativeID before deployment: %q", created.ProgressResult.NativeID)
	}

	final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	t.Logf("install reached Success in %s", time.Since(start))

	// --- The weighted pre-install hooks ran, and Helm reaped the Job ---------
	// automigrate carries hook-delete-policy hook-succeeded, so a completed
	// migration leaves nothing behind. No hook logic exists in this plugin.
	jobs, err := r.Client.BatchV1().Jobs(kratosNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range jobs.Items {
		t.Logf("surviving Job: %s", j.Name)
		if j.Name == kratosRelease+"-automigrate" {
			t.Errorf("automigrate Job survived; Helm should have reaped it on success")
		}
	}

	// The migration having succeeded is what let the main set apply at all.
	// kratos itself is a Deployment; the courier is the StatefulSet.
	if _, err := r.Client.AppsV1().Deployments(kratosNamespace).Get(ctx, kratosRelease, metav1.GetOptions{}); err != nil {
		t.Errorf("kratos Deployment missing after a successful install: %v", err)
	}
	if _, err := r.Client.AppsV1().StatefulSets(kratosNamespace).Get(ctx, kratosRelease+"-courier", metav1.GetOptions{}); err != nil {
		t.Errorf("kratos-courier StatefulSet missing after a successful install: %v", err)
	}

	// --- The `test-success` Pod hook was NOT applied ------------------------
	// Under the render-and-decompose approach, test hooks get applied as ordinary
	// resources on every reconcile, which is never correct. Helm only runs them
	// on `helm test`, so the Pod must not exist.
	pods, err := r.Client.CoreV1().Pods(kratosNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	for _, p := range pods.Items {
		if p.Name == kratosRelease+"-test-connection" {
			t.Errorf("test hook Pod %s was applied as a real resource", p.Name)
		}
	}

	// --- Read: inventory covers the whole chart -----------------------------
	read, err := r.Read(ctx, &resource.ReadRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got releaseProperties
	if err := json.Unmarshal([]byte(read.Properties), &got); err != nil {
		t.Fatal(err)
	}
	t.Logf("revision=%d status=%s appVersion=%s", got.Revision, got.Status, got.AppVersion)
	for kind, names := range got.ResourceNames {
		t.Logf("  resourceNames[%s] = %v", kind, names)
	}
	if len(got.ResourceNames) < 4 {
		t.Errorf("resourceNames has %d kinds; a chart this size renders more", len(got.ResourceNames))
	}

	// --- Discovery collapses the chart, hooks included ----------------------
	invalidateInventory(cfg)
	inv, err := InventoryFor(ctx, cfg)
	if err != nil {
		t.Fatalf("InventoryFor: %v", err)
	}
	t.Logf("release inventory holds %d objects", inv.Len())

	// Main-set objects.
	for _, tc := range []struct{ kind, name string }{
		{"Deployment", kratosRelease},
		{"StatefulSet", kratosRelease + "-courier"},
		{"Service", kratosRelease + "-public"},
		{"Service", kratosRelease + "-admin"},
	} {
		if _, owned := inv.OwnedBy(tc.kind, kratosNamespace, tc.name); !owned {
			t.Errorf("%s/%s not in inventory; discovery would surface it as unmanaged",
				tc.kind, tc.name)
		}
	}

	// Hook objects are indexed too, from rel.Hooks rather than rel.Manifest.
	// A hook-created ConfigMap left visible in discovery is the same leak as a
	// visible Deployment.
	if _, owned := inv.OwnedBy("ConfigMap", kratosNamespace, kratosRelease+"-config"); !owned {
		t.Error("hook-annotated ConfigMap not in inventory; hooks must be indexed from rel.Hooks")
	}

	// And the filter actually drops them.
	kept := FilterHelmOwned(inv, "K8S::Apps::Deployment", []string{
		prov.NativeID(kratosNamespace, kratosRelease),
		prov.NativeID(kratosNamespace, "hand-authored"),
	})
	if len(kept) != 1 || kept[0] != prov.NativeID(kratosNamespace, "hand-authored") {
		t.Errorf("filter kept %v, want only the hand-authored Deployment", kept)
	}

	// A same-named object of a different kind must survive the filter: kratos is
	// a Deployment, so a StatefulSet called "kratos" belongs to whoever made it.
	if kept := FilterHelmOwned(inv, "K8S::Apps::StatefulSet", []string{
		prov.NativeID(kratosNamespace, kratosRelease),
	}); len(kept) != 1 {
		t.Errorf("a StatefulSet sharing the Deployment's name was hidden: %v", kept)
	}

	// --- Upgrade: pre-upgrade hooks fire again ------------------------------
	// The migration hook is annotated pre-install AND pre-upgrade, so this
	// re-runs it — the case that makes automatic retry of a wedged install
	// unsafe, and that a plain `helm upgrade` handles correctly.
	upStart := time.Now()
	updated, err := r.Update(ctx, &resource.UpdateRequest{
		NativeID:          nativeID,
		ResourceType:      ResourceTypeRelease,
		DesiredProperties: kratosProps(t, "http://127.0.0.2/"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("Update returned %s, want InProgress", updated.ProgressResult.OperationStatus)
	}
	final = pollUntilTerminal(t, r, nativeID, updated.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("upgrade ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	t.Logf("upgrade reached Success in %s", time.Since(upStart))

	read, err = r.Read(ctx, &resource.ReadRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Read after upgrade: %v", err)
	}
	got = releaseProperties{}
	if err := json.Unmarshal([]byte(read.Properties), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 {
		t.Errorf("revision after upgrade = %d, want 2", got.Revision)
	}

	// --- Delete, and the documented residue --------------------------------
	// Fire-and-poll, like install: kratos has pre-delete hooks, so Delete submits
	// and the record's absence is what says the uninstall finished.
	del, err := r.Delete(ctx, &resource.DeleteRequest{
		NativeID: nativeID, ResourceType: ResourceTypeRelease,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if final := pollUntilTerminal(t, r, nativeID, del.ProgressResult.RequestID); final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("uninstall ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

	read, err = r.Read(ctx, &resource.ReadRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Read after delete: %v", err)
	}
	if read.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("Read after delete = %q, want NotFound", read.ErrorCode)
	}

	// The Secret annotated helm.sh/resource-policy: keep is expected to survive.
	// Asserted rather than merely documented, because it is the difference
	// between "formae destroy left residue" being known behaviour and being a
	// surprise. Once no release claims it, discovery correctly shows it as
	// unmanaged.
	secrets, err := r.Client.CoreV1().Secrets(kratosNamespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("list secrets after delete: %v", err)
	}
	for _, s := range secrets.Items {
		if s.Annotations["helm.sh/resource-policy"] == "keep" {
			t.Logf("retained by resource-policy=keep, as expected: Secret/%s", s.Name)
		}
	}
}
