//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Live-cluster exercise of the K8S::Helm::Release lifecycle against whatever
// cluster the ambient kubeconfig points at.
//
// The chart under test (testdata/charts/hooked) carries a pre-install hook Job,
// because that is the case the render-and-decompose path gets wrong and the
// whole reason this resource exists. A pass here means Helm scheduled and reaped
// the hook with no hook logic in this plugin.
package helm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	apicorev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	testNamespace = "formae-helm-release-it"
	testRelease   = "hooked"
)

func newTestRelease(t *testing.T) (*Release, *config.Config) {
	t.Helper()

	cfg, err := config.FromTargetConfig([]byte(`{"Auth":{"Type":"Kubeconfig"}}`))
	if err != nil {
		t.Fatalf("FromTargetConfig: %v", err)
	}
	client, err := transport.NewClient(cfg)
	if err != nil {
		t.Skipf("no reachable cluster: %v", err)
	}
	if _, err := client.Discovery().ServerVersion(); err != nil {
		t.Skipf("no reachable cluster: %v", err)
	}
	return &Release{Client: client, Config: cfg}, cfg
}

func chartPath(t *testing.T) string {
	t.Helper()
	// pkg/resources/helm -> repo root
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "charts", "hooked"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p, "Chart.yaml")); err != nil {
		t.Fatalf("test chart missing at %s: %v", p, err)
	}
	return p
}

func props(t *testing.T, chart, message string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": testRelease, "namespace": testNamespace},
		"chart":    chart,
		"values":   map[string]any{"message": message},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pollUntilTerminal drives Status the way formae's orchestrator does, until the
// operation leaves InProgress.
func pollUntilTerminal(t *testing.T, r *Release, nativeID, requestID string) *resource.ProgressResult {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	var last *resource.ProgressResult
	for time.Now().Before(deadline) {
		res, err := r.Status(context.Background(), &resource.StatusRequest{
			RequestID:    requestID,
			NativeID:     nativeID,
			ResourceType: ResourceTypeRelease,
		})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		last = res.ProgressResult
		if last.OperationStatus != resource.OperationStatusInProgress {
			return last
		}
		t.Logf("in progress: %s", last.StatusMessage)
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out waiting for %s; last status: %+v", requestID, last)
	return nil
}

func TestReleaseLifecycle(t *testing.T) {
	r, cfg := newTestRelease(t)
	ctx := context.Background()
	nsClient := r.Client.CoreV1().Namespaces()

	ensureNamespace(t, nsClient)
	t.Cleanup(func() {
		_ = nsClient.Delete(context.Background(), testNamespace, metav1.DeleteOptions{})
	})

	nativeID := prov.NativeID(testNamespace, testRelease)
	chart := chartPath(t)

	// --- Create: must return promptly, not block on the pre-install hook ------
	start := time.Now()
	created, err := r.Create(ctx, &resource.CreateRequest{
		ResourceType: ResourceTypeRelease,
		Properties:   props(t, chart, "hello"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	submitDuration := time.Since(start)
	t.Logf("Create returned in %s with request id %q", submitDuration, created.ProgressResult.RequestID)

	if created.ProgressResult.OperationStatus != resource.OperationStatusInProgress {
		t.Fatalf("Create returned %s, want InProgress", created.ProgressResult.OperationStatus)
	}
	// Withheld on purpose: formae records a resource once it has a NativeID, and
	// a release at pending-install is not deployed yet.
	if created.ProgressResult.NativeID != "" {
		t.Errorf("Create returned NativeID %q; it must be withheld until deployed",
			created.ProgressResult.NativeID)
	}
	// Which makes the RequestID the only handle Status has.
	if created.ProgressResult.RequestID == "" {
		t.Fatal("Create returned neither a NativeID nor a RequestID; Status has nothing to poll")
	}
	// The whole point of fire-and-poll: submit must not wait out the hook Job.
	if submitDuration > 20*time.Second {
		t.Errorf("Create blocked for %s — fire-and-poll is not working", submitDuration)
	}

	// Poll with an empty NativeID, exactly as the host will: it forwards
	// whatever Create returned (plugin_operator.go:238-248).
	final := pollUntilTerminal(t, r, "", created.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("install ended %s: %s", final.OperationStatus, final.StatusMessage)
	}
	// Only now does the release earn its NativeID, which is when formae records
	// it as managed.
	if final.NativeID != nativeID {
		t.Fatalf("NativeID on success = %q, want %q", final.NativeID, nativeID)
	}
	if len(final.ResourceProperties) == 0 {
		t.Error("no ResourceProperties on success; formae would record an empty resource")
	}

	// --- Read: values round-trip, inventory is exposed -----------------------
	read, err := r.Read(ctx, &resource.ReadRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var got releaseProperties
	if err := json.Unmarshal([]byte(read.Properties), &got); err != nil {
		t.Fatalf("unmarshal read properties: %v", err)
	}
	if got.Revision != 1 {
		t.Errorf("revision = %d, want 1", got.Revision)
	}
	if got.Status != "deployed" {
		t.Errorf("status = %q, want deployed", got.Status)
	}
	if msg, _ := got.Values["message"].(string); msg != "hello" {
		t.Errorf("values.message = %v, want hello", got.Values["message"])
	}
	if len(got.ResourceNames) == 0 {
		t.Error("resourceNames is empty; a collapsed release must still say what it owns")
	}
	t.Logf("resourceNames: %v", got.ResourceNames)

	// --- The hook ran and Helm reaped it ------------------------------------
	// hook-delete-policy: hook-succeeded. No hook logic in this plugin.
	jobs, err := r.Client.BatchV1().Jobs(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range jobs.Items {
		if j.Name == testRelease+"-preinstall" {
			t.Errorf("hook Job %s still present; Helm should have reaped it", j.Name)
		}
	}
	// The hook having run is what let the release reach deployed at all, and the
	// chart's ConfigMap proves the main set applied after it.
	if _, err := r.Client.CoreV1().ConfigMaps(testNamespace).Get(ctx, testRelease+"-config", metav1.GetOptions{}); err != nil {
		t.Errorf("chart ConfigMap missing: %v", err)
	}

	// --- The release is a real Helm release, not a formae lookalike ----------
	// `helm list` selects on owner=helm over Secrets of type helm.sh/release.v1
	// named sh.helm.release.v1.<name>.v<revision>. Matching that shape is what
	// makes `helm list`, `helm history` and `helm rollback` work against a
	// release formae created — and it comes free from driving the SDK, rather
	// than from hand-forging the Secret.
	secretName := "sh.helm.release.v1." + testRelease + ".v1"
	sec, err := r.Client.CoreV1().Secrets(testNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("helm release Secret %s missing: %v", secretName, err)
	}
	if string(sec.Type) != "helm.sh/release.v1" {
		t.Errorf("release Secret type = %q, want helm.sh/release.v1", sec.Type)
	}
	if sec.Labels["owner"] != "helm" {
		t.Errorf("release Secret owner label = %q, want helm — `helm list` selects on it", sec.Labels["owner"])
	}
	t.Logf("helm release Secret %s: type=%s labels=%v", secretName, sec.Type, sec.Labels)

	// --- Discovery collapses the chart's objects ----------------------------
	invalidateInventory(cfg)
	inv, err := InventoryFor(ctx, cfg)
	if err != nil {
		t.Fatalf("InventoryFor: %v", err)
	}
	if _, owned := inv.OwnedBy("ConfigMap", testNamespace, testRelease+"-config"); !owned {
		t.Error("chart ConfigMap not in the release inventory, so discovery will surface it as unmanaged")
	}
	kept := FilterHelmOwned(inv, "K8S::Core::ConfigMap", []string{
		prov.NativeID(testNamespace, testRelease+"-config"),
		prov.NativeID(testNamespace, "not-from-a-chart"),
	})
	if len(kept) != 1 || kept[0] != prov.NativeID(testNamespace, "not-from-a-chart") {
		t.Errorf("filter kept %v, want only the non-chart ConfigMap", kept)
	}

	// --- List sees the release ----------------------------------------------
	listed, err := r.List(ctx, &resource.ListRequest{
		ResourceType:         ResourceTypeRelease,
		AdditionalProperties: map[string]string{"namespace": testNamespace},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !contains(listed.NativeIDs, nativeID) {
		t.Errorf("List returned %v, missing %q", listed.NativeIDs, nativeID)
	}

	// --- Update: revision advances -----------------------------------------
	updated, err := r.Update(ctx, &resource.UpdateRequest{
		NativeID:          nativeID,
		ResourceType:      ResourceTypeRelease,
		DesiredProperties: props(t, chart, "goodbye"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Asymmetric with Create by design: the resource is already in formae's
	// state, so there is nothing to withhold and dropping the handle mid-upgrade
	// would only risk losing it.
	if updated.ProgressResult.NativeID != nativeID {
		t.Errorf("Update NativeID = %q, want %q — an upgrade must keep the handle",
			updated.ProgressResult.NativeID, nativeID)
	}
	final = pollUntilTerminal(t, r, nativeID, updated.ProgressResult.RequestID)
	if final.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("upgrade ended %s: %s", final.OperationStatus, final.StatusMessage)
	}

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
	if msg, _ := got.Values["message"].(string); msg != "goodbye" {
		t.Errorf("values.message after upgrade = %v, want goodbye", got.Values["message"])
	}

	// --- Delete -------------------------------------------------------------
	del, err := r.Delete(ctx, &resource.DeleteRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if del.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("Delete returned %s", del.ProgressResult.OperationStatus)
	}

	// Read must now report NotFound so formae drops it from state.
	read, err = r.Read(ctx, &resource.ReadRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease})
	if err != nil {
		t.Fatalf("Read after delete: %v", err)
	}
	if read.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("Read after delete returned %q, want NotFound", read.ErrorCode)
	}

	// Delete on an absent release is a no-op success, not an error — formae
	// retries Delete and a second call must not fail the operation.
	if _, err := r.Delete(ctx, &resource.DeleteRequest{NativeID: nativeID, ResourceType: ResourceTypeRelease}); err != nil {
		t.Errorf("second Delete errored: %v", err)
	}
}

func ensureNamespace(t *testing.T, nsClient corev1.NamespaceInterface) {
	t.Helper()
	_, err := nsClient.Get(context.Background(), testNamespace, metav1.GetOptions{})
	if err == nil {
		return
	}
	ns := &apicorev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	if _, err := nsClient.Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", testNamespace, err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
