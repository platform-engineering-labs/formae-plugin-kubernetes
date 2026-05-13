//go:build integration

package networking_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/networking"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIngressCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Networking::Ingress",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "Ingress",
				"metadata": map[string]any{
					"name":      "test-ingress",
					"namespace": ns,
				},
				"spec": map[string]any{
					"rules": []map[string]any{
						{
							"host": "test.example.com",
							"http": map[string]any{
								"paths": []map[string]any{
									{
										"path":     "/",
										"pathType": "Prefix",
										"backend": map[string]any{
											"service": map[string]any{
												"name": "test-service",
												"port": map[string]any{"number": 80},
											},
										},
									},
								},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "Ingress",
				"metadata": map[string]any{
					"name":      "test-ingress",
					"namespace": ns,
				},
				"spec": map[string]any{
					"rules": []map[string]any{
						{
							"host": "updated.example.com",
							"http": map[string]any{
								"paths": []map[string]any{
									{
										"path":     "/",
										"pathType": "Prefix",
										"backend": map[string]any{
											"service": map[string]any{
												"name": "test-service",
												"port": map[string]any{"number": 8080},
											},
										},
									},
								},
							},
						},
					},
				},
			})
		},
		// LoadBalancerTimeoutSeconds unset, so always Success
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestIngressClassCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Networking::IngressClass",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "IngressClass",
				"metadata": map[string]any{
					"name":   "formae-int-ic-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"spec": map[string]any{
					"controller": "example.com/formae-test",
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "IngressClass",
				"metadata": map[string]any{
					"name":   "formae-int-ic-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"spec": map[string]any{
					"controller": "example.com/formae-test",
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
			_ = env.Client.NetworkingV1().IngressClasses().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestNetworkPolicyCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Networking::NetworkPolicy",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "NetworkPolicy",
				"metadata": map[string]any{
					"name":      "test-netpol",
					"namespace": ns,
				},
				"spec": map[string]any{
					"podSelector": map[string]any{
						"matchLabels": map[string]string{"app": "test"},
					},
					"policyTypes": []string{"Ingress"},
					"ingress": []map[string]any{
						{
							"from": []map[string]any{
								{"podSelector": map[string]any{"matchLabels": map[string]string{"role": "frontend"}}},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "networking.k8s.io/v1",
				"kind":       "NetworkPolicy",
				"metadata": map[string]any{
					"name":      "test-netpol",
					"namespace": ns,
				},
				"spec": map[string]any{
					"podSelector": map[string]any{
						"matchLabels": map[string]string{"app": "test"},
					},
					"policyTypes": []string{"Ingress"},
					"ingress": []map[string]any{
						{
							"from": []map[string]any{
								{"podSelector": map[string]any{"matchLabels": map[string]string{"role": "backend"}}},
							},
						},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}
