//go:build integration

package scheduling_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/scheduling"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPriorityClassCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Scheduling::PriorityClass",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "scheduling.k8s.io/v1",
				"kind":       "PriorityClass",
				"metadata": map[string]any{
					"name":   "formae-int-pc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"value":         1000,
				"globalDefault": false,
				"description":   "Integration test priority class",
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "scheduling.k8s.io/v1",
				"kind":       "PriorityClass",
				"metadata": map[string]any{
					"name":   "formae-int-pc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"value":         1000,
				"globalDefault": false,
				"description":   "Updated integration test priority class",
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.SchedulingV1().PriorityClasses().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
