//go:build integration

package policy_test

import (
	"encoding/json"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/policy"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestPodDisruptionBudgetCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Policy::PodDisruptionBudget",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "policy/v1",
				"kind":       "PodDisruptionBudget",
				"metadata": map[string]any{
					"name":      "test-pdb",
					"namespace": ns,
				},
				"spec": map[string]any{
					"minAvailable": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test"},
					},
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "policy/v1",
				"kind":       "PodDisruptionBudget",
				"metadata": map[string]any{
					"name":      "test-pdb",
					"namespace": ns,
				},
				"spec": map[string]any{
					"minAvailable": 2,
					"selector": map[string]any{
						"matchLabels": map[string]string{"app": "test"},
					},
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}
