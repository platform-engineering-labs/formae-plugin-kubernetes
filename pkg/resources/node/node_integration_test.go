//go:build integration

package node_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/node"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRuntimeClassCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Node::RuntimeClass",
		IsNamespaced: false,
		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "node.k8s.io/v1",
				"kind":       "RuntimeClass",
				"metadata": map[string]any{
					"name":   "formae-int-rc-" + ns,
					"labels": map[string]string{"app": "test"},
				},
				"handler": "runc",
			})
		},
		UpdateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "node.k8s.io/v1",
				"kind":       "RuntimeClass",
				"metadata": map[string]any{
					"name":   "formae-int-rc-" + ns,
					"labels": map[string]string{"app": "updated"},
				},
				"handler": "runc",
			})
		},
		ExpectedCreateStatus: resource.OperationStatusSuccess,
		ExpectedFinalStatus:  resource.OperationStatusSuccess,
		StatusTimeout:        10 * time.Second,
		CleanupExtra: func(t *testing.T, env *testutil.TestEnv, nativeID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := strings.TrimPrefix(nativeID, "/")
			_ = env.Client.NodeV1().RuntimeClasses().Delete(ctx, name, metav1.DeleteOptions{})
		},
	})
}
