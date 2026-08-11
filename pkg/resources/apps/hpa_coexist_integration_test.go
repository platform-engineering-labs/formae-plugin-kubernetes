//go:build integration

package apps_test

import (
	"context"
	"strings"
	"testing"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func scaleTo(ns, name string, n int32) *autoscalingv1.Scale {
	return &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       autoscalingv1.ScaleSpec{Replicas: n},
	}
}

func TestDeploymentOmittedReplicasNotDriftedByHPA(t *testing.T) {
	env := testutil.SetupEnv(t)
	p := env.NewProvisioner("K8S::Apps::Deployment")
	ns := env.Namespace
	ctx := context.Background()

	// Deployment WITHOUT spec.replicas (HPA will own the count).
	props := testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "hpa-app", "namespace": ns},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]string{"app": "hpa-app"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app": "hpa-app"}},
				"spec":     map[string]any{"containers": []map[string]any{{"name": "c", "image": "nginx:1.27"}}},
			},
		},
	})
	cr, err := p.Create(ctx, &resource.CreateRequest{ResourceType: "K8S::Apps::Deployment", Properties: props})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nid := cr.ProgressResult.NativeID
	t.Cleanup(func() {
		_, _ = p.Delete(ctx, &resource.DeleteRequest{NativeID: nid, ResourceType: "K8S::Apps::Deployment"})
	})

	// Create an HPA that owns replicas and scale externally.
	minR := int32(2)
	_, err = env.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "hpa-app"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "hpa-app"},
			MinReplicas:    &minR, MaxReplicas: 5,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("hpa create: %v", err)
	}
	t.Cleanup(func() {
		_ = env.Client.AutoscalingV2().HorizontalPodAutoscalers(ns).Delete(ctx, "hpa-app", metav1.DeleteOptions{})
	})

	// Simulate the HPA scaling the Deployment up, taking ownership of replicas.
	if _, err := env.Client.AppsV1().Deployments(ns).UpdateScale(ctx, "hpa-app", scaleTo(ns, "hpa-app", 4), metav1.UpdateOptions{FieldManager: "hpa-controller"}); err != nil {
		t.Fatalf("external scale: %v", err)
	}

	// Read back: properties must NOT contain replicas (formae doesn't own it).
	rr, err := p.Read(ctx, &resource.ReadRequest{NativeID: nid, ResourceType: "K8S::Apps::Deployment"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(rr.Properties, "\"replicas\"") {
		t.Fatalf("expected replicas stripped from unowned Deployment, got: %s", rr.Properties)
	}
}
