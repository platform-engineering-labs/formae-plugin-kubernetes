// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mf(manager, fieldsJSON string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{Manager: manager, FieldsV1: metav1.NewFieldsV1(fieldsJSON)}
}

func TestManagerOwnsField_True(t *testing.T) {
	fields := []metav1.ManagedFieldsEntry{mf("formae", `{"f:spec":{"f:replicas":{}}}`)}
	if !ManagerOwnsField(fields, "formae", "spec", "replicas") {
		t.Fatal("expected formae to own spec.replicas")
	}
}

func TestManagerOwnsField_FalseWhenOtherManager(t *testing.T) {
	fields := []metav1.ManagedFieldsEntry{
		mf("formae", `{"f:spec":{"f:template":{}}}`),
		mf("hpa-controller", `{"f:spec":{"f:replicas":{}}}`),
	}
	if ManagerOwnsField(fields, "formae", "spec", "replicas") {
		t.Fatal("expected formae NOT to own spec.replicas (HPA owns it)")
	}
}

func TestStripUnownedReplicas_RemovesWhenUnowned(t *testing.T) {
	props := json.RawMessage(`{"spec":{"replicas":5,"selector":{}}}`)
	fields := []metav1.ManagedFieldsEntry{mf("formae", `{"f:spec":{"f:selector":{}}}`)}
	out := StripUnownedReplicas(props, fields)
	if strings.Contains(string(out), "replicas") {
		t.Fatalf("expected replicas stripped, got %s", out)
	}
}

func TestStripUnownedReplicas_KeepsWhenOwned(t *testing.T) {
	props := json.RawMessage(`{"spec":{"replicas":2}}`)
	fields := []metav1.ManagedFieldsEntry{mf("formae", `{"f:spec":{"f:replicas":{}}}`)}
	out := StripUnownedReplicas(props, fields)
	if !strings.Contains(string(out), "replicas") {
		t.Fatalf("expected replicas kept, got %s", out)
	}
}
