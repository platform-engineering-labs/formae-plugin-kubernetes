//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package custom_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/custom"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

const customType = "K8S::Custom::Resource"

// crdManifest defines a minimal namespaced Widget CRD in group example.com.
func crdManifest(t *testing.T) json.RawMessage {
	return testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "widgets.example.com"},
		"spec": map[string]any{
			"group": "example.com",
			"names": map[string]any{
				"kind":     "Widget",
				"listKind": "WidgetList",
				"plural":   "widgets",
				"singular": "widget",
			},
			"scope": "Namespaced",
			"versions": []any{
				map[string]any{
					"name":    "v1",
					"served":  true,
					"storage": true,
					"schema": map[string]any{
						"openAPIV3Schema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"spec": map[string]any{
									"type":       "object",
									"properties": map[string]any{"size": map[string]any{"type": "integer"}},
								},
							},
						},
					},
				},
			},
		},
	})
}

func widgetManifest(t *testing.T, ns string, size int) json.RawMessage {
	return testutil.MustMarshalJSON(t, map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "widget-1", "namespace": ns},
		"spec":       map[string]any{"size": size},
	})
}

// TestCustomResource_DeployCRDThenInstanceAndDiscover is the north-star
// acceptance test: deploy a CRD via the catch-all provisioner, then create,
// read, discover, update, and delete an instance of that CRD — all in one
// session, exercising the RESTMapper reset-on-miss path.
func TestCustomResource_DeployCRDThenInstanceAndDiscover(t *testing.T) {
	env := testutil.SetupEnv(t)
	ctx := context.Background()
	p := env.NewProvisioner(customType)

	// 1. Deploy the CRD itself (cluster-scoped) through the catch-all.
	crdRes, err := p.Create(ctx, &resource.CreateRequest{ResourceType: customType, Properties: crdManifest(t)})
	if err != nil {
		t.Fatalf("create CRD: %v", err)
	}
	testutil.RequireSuccess(t, crdRes.ProgressResult, "create CRD")
	crdID := crdRes.ProgressResult.NativeID
	if crdID != "apiextensions.k8s.io/v1/CustomResourceDefinition//widgets.example.com" {
		t.Fatalf("unexpected CRD NativeID: %q", crdID)
	}
	t.Cleanup(func() {
		_, _ = p.Delete(context.Background(), &resource.DeleteRequest{ResourceType: customType, NativeID: crdID})
	})

	// 2. Create a Widget instance. The CRD endpoint may take a moment to be
	// served; ResolveMapping resets-on-miss, so retry Create until it lands.
	var widgetRes *resource.CreateResult
	deadline := time.Now().Add(30 * time.Second)
	for {
		widgetRes, err = p.Create(ctx, &resource.CreateRequest{ResourceType: customType, Properties: widgetManifest(t, env.Namespace, 3)})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("create Widget after CRD never succeeded: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	testutil.RequireSuccess(t, widgetRes.ProgressResult, "create Widget")
	widgetID := widgetRes.ProgressResult.NativeID
	wantID := prov.CustomResourceID("example.com/v1", "Widget", env.Namespace, "widget-1")
	if widgetID != wantID {
		t.Fatalf("Widget NativeID = %q, want %q", widgetID, wantID)
	}
	if !strings.Contains(string(widgetRes.ProgressResult.ResourceProperties), `"formaeId":"`+wantID+`"`) {
		t.Errorf("Widget properties missing formaeId %q: %s", wantID, widgetRes.ProgressResult.ResourceProperties)
	}

	// 3. Read it back by NativeID (recovers GVK from the ID).
	readRes, err := p.Read(ctx, &resource.ReadRequest{ResourceType: customType, NativeID: widgetID})
	if err != nil {
		t.Fatalf("read Widget: %v", err)
	}
	testutil.RequireReadProperties(t, readRes, "read Widget")

	// 4. Discover: List enumerates instances of every installed CRD; the Widget
	// must be among them.
	listRes, err := p.List(ctx, &resource.ListRequest{ResourceType: customType})
	if err != nil {
		t.Fatalf("list/discover: %v", err)
	}
	found := false
	for _, id := range listRes.NativeIDs {
		if id == widgetID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("discovery did not surface Widget %q; got %v", widgetID, listRes.NativeIDs)
	}

	// 5. Update the Widget spec.
	updRes, err := p.Update(ctx, &resource.UpdateRequest{ResourceType: customType, DesiredProperties: widgetManifest(t, env.Namespace, 7)})
	if err != nil {
		t.Fatalf("update Widget: %v", err)
	}
	testutil.RequireSuccess(t, updRes.ProgressResult, "update Widget")
	if !strings.Contains(string(updRes.ProgressResult.ResourceProperties), `"size":7`) {
		t.Errorf("update did not reflect size=7: %s", updRes.ProgressResult.ResourceProperties)
	}

	// 6. Delete the Widget.
	delRes, err := p.Delete(ctx, &resource.DeleteRequest{ResourceType: customType, NativeID: widgetID})
	if err != nil {
		t.Fatalf("delete Widget: %v", err)
	}
	testutil.RequireSuccess(t, delRes.ProgressResult, "delete Widget")
}
