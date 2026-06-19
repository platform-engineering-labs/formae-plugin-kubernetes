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
			"managedFields":     []interface{}{map[string]interface{}{"manager": "formae"}},
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
	for _, f := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields"} {
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

func TestCustomLiveState_StripsCRDConversionDefault(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "widgets.example.com"},
		"spec": map[string]interface{}{
			"group":      "example.com",
			"scope":      "Namespaced",
			"conversion": map[string]interface{}{"strategy": "None"}, // apiserver default
		},
	}
	out, err := CustomLiveState(obj)
	if err != nil {
		t.Fatalf("CustomLiveState error: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spec := got["spec"].(map[string]interface{})
	if _, ok := spec["conversion"]; ok {
		t.Error("spec.conversion (apiserver default) should be stripped from a CRD")
	}
	if spec["group"] != "example.com" {
		t.Error("user-set spec fields must be preserved")
	}
}

func TestCustomLiveState_KeepsConversionOnNonCRD(t *testing.T) {
	// A custom resource that happens to have a spec.conversion field must keep it
	// — the strip only applies to CustomResourceDefinition objects.
	obj := map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w", "namespace": "default"},
		"spec":       map[string]interface{}{"conversion": "user-data"},
	}
	out, err := CustomLiveState(obj)
	if err != nil {
		t.Fatalf("CustomLiveState error: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["spec"].(map[string]interface{})["conversion"] != "user-data" {
		t.Error("non-CRD spec.conversion must be preserved")
	}
}
