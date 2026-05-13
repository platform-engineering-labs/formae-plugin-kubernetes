//go:build integration

package rbac_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/rbac"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoleCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Rbac::Role",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "Role",
				"metadata": map[string]any{
					"name":      "test-role",
					"namespace": ns,
				},
				"rules": []map[string]any{
					{
						"apiGroups": []string{""},
						"resources": []string{"pods"},
						"verbs":     []string{"get", "list"},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "Role",
				"metadata": map[string]any{
					"name":      "test-role",
					"namespace": ns,
				},
				"rules": []map[string]any{
					{
						"apiGroups": []string{""},
						"resources": []string{"pods", "services"},
						"verbs":     []string{"get", "list", "watch"},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestRoleBindingCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Rbac::RoleBinding",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "RoleBinding",
				"metadata": map[string]any{
					"name":      "test-rolebinding",
					"namespace": ns,
					"labels":    map[string]string{"app": "test"},
				},
				"roleRef": map[string]any{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "ClusterRole",
					"name":     "view",
				},
				"subjects": []map[string]any{
					{"kind": "ServiceAccount", "name": "default", "namespace": ns},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "RoleBinding",
				"metadata": map[string]any{
					"name":      "test-rolebinding",
					"namespace": ns,
					"labels":    map[string]string{"app": "updated"},
				},
				"roleRef": map[string]any{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "ClusterRole",
					"name":     "view",
				},
				"subjects": []map[string]any{
					{"kind": "ServiceAccount", "name": "default", "namespace": ns},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}

func TestClusterRoleCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Rbac::ClusterRole",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "ClusterRole",
				"metadata": map[string]any{
					"name":   "formae-int-cr-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"rules": []map[string]any{
					{
						"apiGroups": []string{""},
						"resources": []string{"pods"},
						"verbs":     []string{"get", "list"},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "ClusterRole",
				"metadata": map[string]any{
					"name":   "formae-int-cr-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"rules": []map[string]any{
					{
						"apiGroups": []string{""},
						"resources": []string{"pods", "services"},
						"verbs":     []string{"get", "list", "watch"},
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
			_ = env.Client.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestClusterRoleBindingCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Rbac::ClusterRoleBinding",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "ClusterRoleBinding",
				"metadata": map[string]any{
					"name":   "formae-int-crb-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"roleRef": map[string]any{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "ClusterRole",
					"name":     "view",
				},
				"subjects": []map[string]any{
					{"kind": "ServiceAccount", "name": "default", "namespace": "default"},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "rbac.authorization.k8s.io/v1",
				"kind":       "ClusterRoleBinding",
				"metadata": map[string]any{
					"name":   "formae-int-crb-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"roleRef": map[string]any{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "ClusterRole",
					"name":     "view",
				},
				"subjects": []map[string]any{
					{"kind": "ServiceAccount", "name": "default", "namespace": "default"},
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
			_ = env.Client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
