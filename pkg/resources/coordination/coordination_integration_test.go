//go:build integration

package coordination_test

import (
	"encoding/json"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/coordination"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestLeaseCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Coordination::Lease",
		IsNamespaced: true,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "coordination.k8s.io/v1",
				"kind":       "Lease",
				"metadata": map[string]any{
					"name":      "test-lease",
					"namespace": ns,
				},
				"spec": map[string]any{
					"holderIdentity":       "test-holder",
					"leaseDurationSeconds": 30,
				},
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "coordination.k8s.io/v1",
				"kind":       "Lease",
				"metadata": map[string]any{
					"name":      "test-lease",
					"namespace": ns,
				},
				"spec": map[string]any{
					"holderIdentity":       "updated-holder",
					"leaseDurationSeconds": 60,
				},
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
	})
}
