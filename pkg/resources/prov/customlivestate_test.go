// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"testing"
)

func TestCustomLiveState_StripsAndInjects(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata": map[string]interface{}{
			"name":              "web",
			"namespace":         "default",
			"uid":               "abc",
			"resourceVersion":   "123",
			"creationTimestamp": "2026-01-01T00:00:00Z",
			"generation":        float64(2),
		},
		"spec":   map[string]interface{}{"secretName": "web-tls"},
		"status": map[string]interface{}{"conditions": []interface{}{}},
	}
	out, err := CustomLiveState(obj)
	if err != nil {
		t.Fatalf("CustomLiveState error: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["status"]; ok {
		t.Error("status should be stripped")
	}
	meta := got["metadata"].(map[string]interface{})
	for _, f := range []string{"uid", "resourceVersion", "creationTimestamp", "generation"} {
		if _, ok := meta[f]; ok {
			t.Errorf("server-managed metadata field %q should be stripped", f)
		}
	}
	if got["formaeId"] != "cert-manager.io/v1/Certificate/default/web" {
		t.Errorf("formaeId = %v, want cert-manager.io/v1/Certificate/default/web", got["formaeId"])
	}
	if got["spec"].(map[string]interface{})["secretName"] != "web-tls" {
		t.Error("spec must be preserved")
	}
}
