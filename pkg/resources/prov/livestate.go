// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// serverManagedMetaFields are metadata fields set by the K8S API server
// that should not appear in LiveState properties. These cause false drift
// detection because PKL-evaluated properties don't include them.
var serverManagedMetaFields = []string{
	"uid",
	"resourceVersion",
	"creationTimestamp",
	"generation",
	"deletionTimestamp",
	"deletionGracePeriodSeconds",
}

// LiveState returns the full live state of a K8S API object as JSON bytes,
// filtered through the typed apply configuration type T with server-managed
// fields stripped.
//
// The apply config round-trip removes most server-only fields, but
// ObjectMetaApplyConfiguration still includes uid, resourceVersion,
// creationTimestamp, generation, etc. We strip these explicitly to prevent
// false "Resource properties changed" detections in Formae.
//
// Use this in Read() and Status() to detect drift from external changes.
// Create/Update should continue using Extract to return only Formae-owned fields.
func LiveState[T any](apiObject any) ([]byte, error) {
	raw, err := json.Marshal(apiObject)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal api object: %w", err)
	}

	var applyConfig T
	if err := json.Unmarshal(raw, &applyConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into apply configuration: %w", err)
	}

	intermediate, err := json.Marshal(applyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal apply configuration: %w", err)
	}

	// Strip server-managed fields that leak through the apply config types
	var result map[string]interface{}
	if err := json.Unmarshal(intermediate, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal for field stripping: %w", err)
	}

	delete(result, "status")

	if meta, ok := result["metadata"].(map[string]interface{}); ok {
		for _, key := range serverManagedMetaFields {
			delete(meta, key)
		}
	}

	return json.Marshal(result)
}
