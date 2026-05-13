//go:build integration

package autoscaling_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/autoscaling"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHorizontalPodAutoscalerCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Autoscaling::HorizontalPodAutoscaler",
		IsNamespaced: true,
		// HPA needs a target Deployment to avoid AbleToScale=False → Failure
		Setup: func(t *testing.T, env *testutil.TestEnv) {
			t.Helper()
			ctx := context.Background()
			replicas := int32(1)
			_, err := env.Client.AppsV1().Deployments(env.Namespace).Create(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "hpa-target", Namespace: env.Namespace},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "hpa-target"}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "hpa-target"}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.27"}}},
					},
				},
			}, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to create HPA target deployment: %v", err)
			}
		},
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "autoscaling/v2",
				"kind":       "HorizontalPodAutoscaler",
				"metadata": map[string]any{
					"name":      "test-hpa",
					"namespace": ns,
				},
				"spec": map[string]any{
					"scaleTargetRef": map[string]any{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"name":       "hpa-target",
					},
					"minReplicas": 1,
					"maxReplicas": 3,
					"metrics": []map[string]any{
						{
							"type": "Resource",
							"resource": map[string]any{
								"name": "cpu",
								"target": map[string]any{
									"type":               "Utilization",
									"averageUtilization": 80,
								},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "autoscaling/v2",
				"kind":       "HorizontalPodAutoscaler",
				"metadata": map[string]any{
					"name":      "test-hpa",
					"namespace": ns,
				},
				"spec": map[string]any{
					"scaleTargetRef": map[string]any{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"name":       "hpa-target",
					},
					"minReplicas": 1,
					"maxReplicas": 5,
					"metrics": []map[string]any{
						{
							"type": "Resource",
							"resource": map[string]any{
								"name": "cpu",
								"target": map[string]any{
									"type":               "Utilization",
									"averageUtilization": 70,
								},
							},
						},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        30 * time.Second,
	})
}
