//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	// Import core to trigger init() registration
	_ "github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/core"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/testutil"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestPodCRUDLifecycle(t *testing.T) {
	testutil.RunCRUDLifecycle(t, testutil.ResourceFixture{
		ResourceType: "K8S::Core::Pod",
		IsNamespaced: true,

		CreateProperties: func(ns string) json.RawMessage {
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]any{
					"name":      "test-pod",
					"namespace": ns,
					"labels": map[string]string{
						"app": "integration-test",
					},
				},
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name":  "nginx",
							"image": "nginx:1.27",
						},
					},
				},
			})
		},

		UpdateProperties: func(ns string) json.RawMessage {
			// Pod only allows image changes on running pods
			return testutil.MustMarshalJSON(t, map[string]any{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]any{
					"name":      "test-pod",
					"namespace": ns,
					"labels": map[string]string{
						"app": "integration-test",
					},
				},
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name":  "nginx",
							"image": "nginx:1.27-alpine",
						},
					},
				},
			})
		},

		ExpectedCreateStatus: resource.OperationStatusInProgress, // Pending phase
		ExpectedFinalStatus:  resource.OperationStatusSuccess,    // Running phase
		StatusTimeout:        90 * time.Second,                   // Image pull may be slow

		VerifyCreate: func(t *testing.T, result *resource.CreateResult) {
			t.Helper()
			nativeID := result.ProgressResult.NativeID
			// NativeID should be namespace/name format
			if !strings.Contains(nativeID, "/") {
				t.Errorf("Create: NativeID %q should contain '/'", nativeID)
			}
			if !strings.HasSuffix(nativeID, "/test-pod") {
				t.Errorf("Create: NativeID %q should end with '/test-pod'", nativeID)
			}
		},

		VerifyRead: func(t *testing.T, result *resource.ReadResult) {
			t.Helper()
			// Verify the properties contain the pod name
			name := testutil.JSONPath(t, result.Properties, "metadata", "name")
			if name != "test-pod" {
				t.Errorf("Read: expected metadata.name=test-pod, got %q", name)
			}
		},

		VerifyUpdate: func(t *testing.T, result *resource.UpdateResult) {
			t.Helper()
			// Verify update returned properties with the new image
			props := string(result.ProgressResult.ResourceProperties)
			if !strings.Contains(props, "nginx:1.27-alpine") {
				t.Errorf("Update: expected properties to contain nginx:1.27-alpine, got %s", props)
			}
		},

		VerifyList: func(t *testing.T, result *resource.ListResult, nativeID string) {
			t.Helper()
			// Just verify we found our pod — the framework already checks nativeID presence
			if len(result.NativeIDs) == 0 {
				t.Error("List: expected at least one NativeID")
			}
		},
	})
}
