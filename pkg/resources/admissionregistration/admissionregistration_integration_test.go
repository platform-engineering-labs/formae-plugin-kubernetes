//go:build integration

package admissionregistration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/admissionregistration"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidatingWebhookConfigurationCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Admissionregistration::ValidatingWebhookConfiguration",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-vwc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"webhooks": []map[string]any{
					{
						"name": "formae-int-vwc." + ns + ".example.com",
						"clientConfig": map[string]any{
							"url": "https://localhost:19443/validate",
						},
						"rules": []map[string]any{
							{
								"apiGroups":   []string{"formae.test.example.com"},
								"apiVersions": []string{"v1"},
								"resources":   []string{"nonexistent"},
								"operations":  []string{"CREATE"},
							},
						},
						"failurePolicy":           "Ignore",
						"sideEffects":              "None",
						"admissionReviewVersions":  []string{"v1"},
						"timeoutSeconds":           5,
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-vwc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"webhooks": []map[string]any{
					{
						"name": "formae-int-vwc." + ns + ".example.com",
						"clientConfig": map[string]any{
							"url": "https://localhost:19443/validate",
						},
						"rules": []map[string]any{
							{
								"apiGroups":   []string{"formae.test.example.com"},
								"apiVersions": []string{"v1"},
								"resources":   []string{"nonexistent"},
								"operations":  []string{"CREATE"},
							},
						},
						"failurePolicy":           "Ignore",
						"sideEffects":              "None",
						"admissionReviewVersions":  []string{"v1"},
						"timeoutSeconds":           10,
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestMutatingWebhookConfigurationCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Admissionregistration::MutatingWebhookConfiguration",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "MutatingWebhookConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-mwc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"webhooks": []map[string]any{
					{
						"name": "formae-int-mwc." + ns + ".example.com",
						"clientConfig": map[string]any{
							"url": "https://localhost:19443/mutate",
						},
						"rules": []map[string]any{
							{
								"apiGroups":   []string{"formae.test.example.com"},
								"apiVersions": []string{"v1"},
								"resources":   []string{"nonexistent"},
								"operations":  []string{"CREATE"},
							},
						},
						"failurePolicy":           "Ignore",
						"sideEffects":              "None",
						"admissionReviewVersions":  []string{"v1"},
						"reinvocationPolicy":       "Never",
						"timeoutSeconds":           5,
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "MutatingWebhookConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-mwc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"webhooks": []map[string]any{
					{
						"name": "formae-int-mwc." + ns + ".example.com",
						"clientConfig": map[string]any{
							"url": "https://localhost:19443/mutate",
						},
						"rules": []map[string]any{
							{
								"apiGroups":   []string{"formae.test.example.com"},
								"apiVersions": []string{"v1"},
								"resources":   []string{"nonexistent"},
								"operations":  []string{"CREATE"},
							},
						},
						"failurePolicy":           "Ignore",
						"sideEffects":              "None",
						"admissionReviewVersions":  []string{"v1"},
						"reinvocationPolicy":       "Never",
						"timeoutSeconds":           10,
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
