//go:build integration

package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/core"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigMapCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::ConfigMap",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-configmap",
					"namespace": ns,
				},
				"data": map[string]string{
					"key1": "value1",
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-configmap",
					"namespace": ns,
				},
				"data": map[string]string{
					"key1": "updated-value",
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		VerifyUpdate: func(t *testing.T, result *resource.UpdateResult) {
			t.Helper()
			props := string(result.ProgressResult.ResourceProperties)
			if !strings.Contains(props, "updated-value") {
				t.Errorf("Update: expected properties to contain updated-value")
			}
		},
	})
}

func TestSecretCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::Secret",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":      "test-secret",
					"namespace": ns,
				},
				"type":       "Opaque",
				"stringData": map[string]string{"key1": "secret-value"},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":      "test-secret",
					"namespace": ns,
				},
				"type":       "Opaque",
				"stringData": map[string]string{"key1": "updated-secret"},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestServiceAccountCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::ServiceAccount",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ServiceAccount",
				"metadata": map[string]any{
					"name":      "test-sa",
					"namespace": ns,
					"labels":    map[string]string{"app": "test"},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ServiceAccount",
				"metadata": map[string]any{
					"name":      "test-sa",
					"namespace": ns,
					"labels":    map[string]string{"app": "updated"},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestServiceCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::Service",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]any{
					"name":      "test-service",
					"namespace": ns,
				},
				"spec": map[string]any{
					"type":     "ClusterIP",
					"selector": map[string]string{"app": "test"},
					"ports": []map[string]any{
						{"port": 80, "targetPort": 8080, "protocol": "TCP"},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]any{
					"name":      "test-service",
					"namespace": ns,
				},
				"spec": map[string]any{
					"type":     "ClusterIP",
					"selector": map[string]string{"app": "test"},
					"ports": []map[string]any{
						{"port": 8080, "targetPort": 9090, "protocol": "TCP"},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestEndpointsCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::Endpoints",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Endpoints",
				"metadata": map[string]any{
					"name":      "test-endpoints",
					"namespace": ns,
				},
				"subsets": []map[string]any{
					{
						"addresses": []map[string]string{{"ip": "10.0.0.1"}},
						"ports":     []map[string]any{{"port": 80, "protocol": "TCP"}},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Endpoints",
				"metadata": map[string]any{
					"name":      "test-endpoints",
					"namespace": ns,
				},
				"subsets": []map[string]any{
					{
						"addresses": []map[string]string{{"ip": "10.0.0.2"}},
						"ports":     []map[string]any{{"port": 8080, "protocol": "TCP"}},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestLimitRangeCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::LimitRange",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "LimitRange",
				"metadata": map[string]any{
					"name":      "test-limitrange",
					"namespace": ns,
				},
				"spec": map[string]any{
					"limits": []map[string]any{
						{
							"type":           "Container",
							"defaultRequest": map[string]string{"memory": "64Mi"},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "LimitRange",
				"metadata": map[string]any{
					"name":      "test-limitrange",
					"namespace": ns,
				},
				"spec": map[string]any{
					"limits": []map[string]any{
						{
							"type":           "Container",
							"defaultRequest": map[string]string{"memory": "128Mi"},
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

func TestResourceQuotaCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::ResourceQuota",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ResourceQuota",
				"metadata": map[string]any{
					"name":      "test-quota",
					"namespace": ns,
				},
				"spec": map[string]any{
					"hard": map[string]string{"pods": "10"},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "ResourceQuota",
				"metadata": map[string]any{
					"name":      "test-quota",
					"namespace": ns,
				},
				"spec": map[string]any{
					"hard": map[string]string{"pods": "20"},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestNamespaceCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::Namespace",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]any{
					"name":   "formae-int-" + ns,
					"labels": map[string]string{"app": "test"},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]any{
					"name":   "formae-int-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        30 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = env.Client.CoreV1().Namespaces().Delete(ctx, nativeID, metav1.DeleteOptions{})
		},
	})
}

func TestPersistentVolumeCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::PersistentVolume",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "PersistentVolume",
				"metadata": map[string]any{
					"name":   "formae-int-pv-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"spec": map[string]any{
					"capacity":    map[string]string{"storage": "1Gi"},
					"accessModes": []string{"ReadWriteOnce"},
					"hostPath":    map[string]string{"path": "/tmp/formae-test"},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "PersistentVolume",
				"metadata": map[string]any{
					"name":   "formae-int-pv-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"spec": map[string]any{
					"capacity":    map[string]string{"storage": "1Gi"},
					"accessModes": []string{"ReadWriteOnce"},
					"hostPath":    map[string]string{"path": "/tmp/formae-test"},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        30 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.CoreV1().PersistentVolumes().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestPersistentVolumeClaimCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::PersistentVolumeClaim",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "PersistentVolumeClaim",
				"metadata": map[string]any{
					"name":      "test-pvc",
					"namespace": ns,
				},
				"spec": map[string]any{
					"accessModes": []string{"ReadWriteOnce"},
					"resources": map[string]any{
						"requests": map[string]string{"storage": "1Gi"},
					},
				},
			})
		},
		SkipUpdate:           true, // PVC spec is immutable after creation
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        30 * time.Second,
	})
}
