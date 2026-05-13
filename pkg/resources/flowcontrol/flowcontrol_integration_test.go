//go:build integration

package flowcontrol_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/flowcontrol"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFlowSchemaCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Flowcontrol::FlowSchema",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "flowcontrol.apiserver.k8s.io/v1",
				"kind":       "FlowSchema",
				"metadata": map[string]any{
					"name":   "formae-int-fs-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"spec": map[string]any{
					"priorityLevelConfiguration": map[string]string{"name": "exempt"},
					"matchingPrecedence":         9999,
					"rules": []map[string]any{
						{
							"subjects": []map[string]any{
								{"kind": "User", "user": map[string]string{"name": "formae-int-nonexistent"}},
							},
							"resourceRules": []map[string]any{
								{
									"apiGroups":  []string{"formae.test.example.com"},
									"resources":  []string{"nonexistent"},
									"verbs":      []string{"get"},
									"namespaces": []string{"*"},
								},
							},
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "flowcontrol.apiserver.k8s.io/v1",
				"kind":       "FlowSchema",
				"metadata": map[string]any{
					"name":   "formae-int-fs-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"spec": map[string]any{
					"priorityLevelConfiguration": map[string]string{"name": "exempt"},
					"matchingPrecedence":         9998,
					"rules": []map[string]any{
						{
							"subjects": []map[string]any{
								{"kind": "User", "user": map[string]string{"name": "formae-int-nonexistent"}},
							},
							"resourceRules": []map[string]any{
								{
									"apiGroups":  []string{"formae.test.example.com"},
									"resources":  []string{"nonexistent"},
									"verbs":      []string{"get"},
									"namespaces": []string{"*"},
								},
							},
						},
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
			_ = env.Client.FlowcontrolV1().FlowSchemas().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}

func TestPriorityLevelConfigurationCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Flowcontrol::PriorityLevelConfiguration",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "flowcontrol.apiserver.k8s.io/v1",
				"kind":       "PriorityLevelConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-plc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"spec": map[string]any{
					"type": "Limited",
					"limited": map[string]any{
						"nominalConcurrencyShares": 10,
						"limitResponse": map[string]any{
							"type": "Reject",
						},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "flowcontrol.apiserver.k8s.io/v1",
				"kind":       "PriorityLevelConfiguration",
				"metadata": map[string]any{
					"name":   "formae-int-plc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"spec": map[string]any{
					"type": "Limited",
					"limited": map[string]any{
						"nominalConcurrencyShares": 20,
						"limitResponse": map[string]any{
							"type": "Reject",
						},
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
			_ = env.Client.FlowcontrolV1().PriorityLevelConfigurations().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
