// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// MetaPatchFunc applies a JSON merge patch to a named K8S resource.
type MetaPatchFunc func(name string, patch []byte) error

// ReconcileMetadata removes labels and annotations from the live resource that
// are not present in the desired apply configuration. This implements
// Terraform-style full-ownership reconciliation: the desired state is the
// complete truth.
//
// If the desired state does not declare labels (nil/absent), live labels are
// left untouched. Only when the desired state explicitly declares labels (even
// an empty map) are extra live labels removed. Same logic applies to annotations.
func ReconcileMetadata(live any, desired any, patchFn MetaPatchFunc) error {
	liveMap, err := toGenericMap(live)
	if err != nil {
		return fmt.Errorf("failed to marshal live object: %w", err)
	}

	desiredMap, err := toGenericMap(desired)
	if err != nil {
		return fmt.Errorf("failed to marshal desired config: %w", err)
	}

	liveMeta, _ := liveMap["metadata"].(map[string]interface{})
	desiredMeta, _ := desiredMap["metadata"].(map[string]interface{})

	if liveMeta == nil {
		return nil
	}

	metaPatch := map[string]interface{}{}

	if removals := extraMapKeys(liveMeta, desiredMeta, "labels"); len(removals) > 0 {
		metaPatch["labels"] = removals
	}

	if removals := extraMapKeys(liveMeta, desiredMeta, "annotations"); len(removals) > 0 {
		metaPatch["annotations"] = removals
	}

	if len(metaPatch) == 0 {
		return nil
	}

	patch := map[string]interface{}{
		"metadata": metaPatch,
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata patch: %w", err)
	}

	name, _ := liveMeta["name"].(string)

	return patchFn(name, patchBytes)
}

// extraMapKeys returns a map with null values for keys in live[field] that are
// absent from desired[field]. Returns nil if there is nothing to remove.
//
// If desired does not contain the field at all (nil), no removals are generated
// — the desired state is not opinionated about that field.
func extraMapKeys(liveMeta, desiredMeta map[string]interface{}, field string) map[string]interface{} {
	liveField, _ := liveMeta[field].(map[string]interface{})
	if len(liveField) == 0 {
		return nil
	}

	// If desired metadata doesn't mention this field, leave live alone.
	if desiredMeta == nil {
		return nil
	}
	if _, specified := desiredMeta[field]; !specified {
		return nil
	}

	desiredField, _ := desiredMeta[field].(map[string]interface{})

	removals := map[string]interface{}{}
	for k := range liveField {
		if _, exists := desiredField[k]; !exists {
			removals[k] = nil // null in JSON merge patch = remove
		}
	}

	if len(removals) == 0 {
		return nil
	}

	return removals
}

func toGenericMap(v any) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	return m, nil
}
