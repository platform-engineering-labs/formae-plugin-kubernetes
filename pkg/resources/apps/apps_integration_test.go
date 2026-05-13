//go:build integration

package apps_test

import (
	"encoding/json"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/apps"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestDeploymentCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Apps::Deployment",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test-deployment",
					"namespace": ns,
				},
				"spec": map[string]any{
					"replicas": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-deploy"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-deploy"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27"},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test-deployment",
					"namespace": ns,
				},
				"spec": map[string]any{
					"replicas": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-deploy"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-deploy"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27-alpine"},
							},
						},
					},
				},
			})
		},
		// Deployment returns Success unless ReplicaFailure condition is True
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        90 * time.Second,
	})
}

func TestReplicaSetCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Apps::ReplicaSet",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "ReplicaSet",
				"metadata": map[string]any{
					"name":      "test-replicaset",
					"namespace": ns,
				},
				"spec": map[string]any{
					"replicas": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-rs"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-rs"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27"},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "ReplicaSet",
				"metadata": map[string]any{
					"name":      "test-replicaset",
					"namespace": ns,
					"labels":    map[string]string{"updated": "true"},
				},
				"spec": map[string]any{
					"replicas": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-rs"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-rs"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27"},
							},
						},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        60 * time.Second,
	})
}

func TestStatefulSetCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Apps::StatefulSet",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "StatefulSet",
				"metadata": map[string]any{
					"name":      "test-statefulset",
					"namespace": ns,
				},
				"spec": map[string]any{
					"serviceName": "test-statefulset",
					"replicas":    1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-sts"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-sts"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27"},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "StatefulSet",
				"metadata": map[string]any{
					"name":      "test-statefulset",
					"namespace": ns,
				},
				"spec": map[string]any{
					"serviceName": "test-statefulset",
					"replicas":    1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-sts"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-sts"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27-alpine"},
							},
						},
					},
				},
			})
		},
		// StatefulSet: Create returns Success (0/0 replicas initially),
		// then InProgress once controller sets Replicas > ReadyReplicas
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        90 * time.Second,
	})
}

func TestDaemonSetCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Apps::DaemonSet",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "DaemonSet",
				"metadata": map[string]any{
					"name":      "test-daemonset",
					"namespace": ns,
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-ds"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-ds"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27"},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "DaemonSet",
				"metadata": map[string]any{
					"name":      "test-daemonset",
					"namespace": ns,
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test-ds"},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]string{"app": "test-ds"},
						},
						"spec": map[string]any{
							"containers": []map[string]any{
								{"name": "nginx", "image": "nginx:1.27-alpine"},
							},
						},
					},
				},
			})
		},
		// DaemonSet: Create returns Success (0/0 scheduled initially),
		// then InProgress once controller sets DesiredNumberScheduled > NumberReady
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        90 * time.Second,
	})
}
