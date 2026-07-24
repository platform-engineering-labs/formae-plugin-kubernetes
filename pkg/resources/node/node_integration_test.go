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

// TestRuntimeClassOverheadInPlaceUpdate proves that `overhead` — de-marked from
// createOnly in the audit — updates in place via SSA without error (K8s accepts
// it), so formae no longer plans a destructive replace when it changes.
func TestRuntimeClassOverheadInPlaceUpdate(t *testing.T) {
	env := testutil.SetupEnv(t)
	p := env.NewProvisioner("K8S::Node::RuntimeClass")
	ctx := context.Background()
	name := "formae-int-rc-inplace-" + env.Namespace

	mk := func(withOverhead bool) json.RawMessage {
		obj := map[string]any{
			"apiVersion": "node.k8s.io/v1",
			"kind":       "RuntimeClass",
			"metadata":   map[string]any{"name": name},
			"handler":    "runc",
		}
		if withOverhead {
			obj["overhead"] = map[string]any{"podFixed": map[string]any{"cpu": "10m"}}
		}
		return testutil.MustMarshalJSON(t, obj)
	}

	cr, err := p.Create(ctx, &resource.CreateRequest{ResourceType: "K8S::Node::RuntimeClass", Properties: mk(false)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nid := cr.ProgressResult.NativeID
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = env.Client.NodeV1().RuntimeClasses().Delete(c, name, metav1.DeleteOptions{})
	})

	ur, err := p.Update(ctx, &resource.UpdateRequest{NativeID: nid, ResourceType: "K8S::Node::RuntimeClass", DesiredProperties: mk(true)})
	if err != nil {
		t.Fatalf("in-place update of overhead failed (would force replace if still createOnly): %v", err)
	}
	if ur.ProgressResult.OperationStatus == resource.OperationStatusFailure {
		t.Fatalf("in-place overhead update reported Failure: %s", ur.ProgressResult.StatusMessage)
	}
}
