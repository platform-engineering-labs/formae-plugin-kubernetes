//go:build integration

package apps_test

import (
	"context"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestStatefulSetReplaceRetainsPVC simulates the destructive half of a core
// replace (delete the StatefulSet) and confirms the volumeClaimTemplates PVC
// survives — Foreground propagation retains PVCs, so an STS replace preserves
// data. This backs the "graceful where K8s allows" policy for immutable,
// data-bearing StatefulSet fields.
func TestStatefulSetReplaceRetainsPVC(t *testing.T) {
	env := testutil.SetupEnv(t)
	p := env.NewProvisioner("K8S::Apps::StatefulSet")
	ns := env.Namespace
	ctx := context.Background()

	props := testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet",
		"metadata": map[string]any{"name": "data-sts", "namespace": ns},
		"spec": map[string]any{
			"serviceName": "data-sts", "replicas": 1,
			"selector": map[string]any{"matchLabels": map[string]string{"app": "data-sts"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app": "data-sts"}},
				"spec":     map[string]any{"containers": []map[string]any{{"name": "c", "image": "nginx:1.27"}}},
			},
			"volumeClaimTemplates": []map[string]any{{
				"metadata": map[string]any{"name": "data"},
				"spec": map[string]any{
					"accessModes": []string{"ReadWriteOnce"},
					"resources":   map[string]any{"requests": map[string]any{"storage": "1Gi"}},
				},
			}},
		},
	})
	cr, err := p.Create(ctx, &resource.CreateRequest{ResourceType: "K8S::Apps::StatefulSet", Properties: props})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nid := cr.ProgressResult.NativeID
	_ = testutil.WaitForStatus(t, p, nid, "K8S::Apps::StatefulSet", cr.ProgressResult.RequestID, 90*time.Second)

	// Delete (the destructive half of a replace).
	if _, err := p.Delete(ctx, &resource.DeleteRequest{NativeID: nid, ResourceType: "K8S::Apps::StatefulSet"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// PVC data-data-sts-0 must still exist.
	pvcs, err := env.Client.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pvc: %v", err)
	}
	found := false
	for _, pvc := range pvcs.Items {
		if pvc.Name == "data-data-sts-0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected VCT PVC data-data-sts-0 to survive StatefulSet delete, got %d PVCs", len(pvcs.Items))
	}
	t.Cleanup(func() {
		_ = env.Client.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, "data-data-sts-0", metav1.DeleteOptions{})
	})
}
