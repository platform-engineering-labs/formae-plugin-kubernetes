// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ManagerOwnsField reports whether the named SSA field manager owns the given
// field path in metadata.managedFields. Path segments are the plain field names
// (e.g. "spec","replicas"); the "f:" managedFields prefix is applied here.
func ManagerOwnsField(managedFields []metav1.ManagedFieldsEntry, manager string, fieldPath ...string) bool {
	for _, entry := range managedFields {
		if entry.Manager != manager || entry.FieldsV1 == nil {
			continue
		}
		var tree map[string]json.RawMessage
		if err := json.Unmarshal(entry.FieldsV1.GetRawBytes(), &tree); err != nil {
			continue
		}
		if walkFieldTree(tree, fieldPath) {
			return true
		}
	}
	return false
}

func walkFieldTree(tree map[string]json.RawMessage, path []string) bool {
	if len(path) == 0 {
		return true
	}
	raw, ok := tree["f:"+path[0]]
	if !ok {
		return false
	}
	if len(path) == 1 {
		return true
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(raw, &child); err != nil {
		return false
	}
	return walkFieldTree(child, path[1:])
}

// StripUnownedReplicas removes spec.replicas from properties when the "formae"
// field manager does not own it — i.e. when an external controller (HPA) owns
// the replica count. This prevents an HPA-scaled count from being reported as
// drift against a forma that omitted replicas.
func StripUnownedReplicas(properties json.RawMessage, managedFields []metav1.ManagedFieldsEntry) json.RawMessage {
	if ManagerOwnsField(managedFields, FieldManager, "spec", "replicas") {
		return properties
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(properties, &obj); err != nil {
		return properties
	}
	specRaw, ok := obj["spec"]
	if !ok {
		return properties
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return properties
	}
	if _, exists := spec["replicas"]; !exists {
		return properties
	}
	delete(spec, "replicas")
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return properties
	}
	obj["spec"] = specBytes
	out, err := json.Marshal(obj)
	if err != nil {
		return properties
	}
	return out
}
