// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// LiveState returns the full live state of a K8S API object as JSON bytes,
// filtered through the typed apply configuration type T. This naturally strips
// server-managed fields (status, managedFields, uid, resourceVersion,
// creationTimestamp, generation, selfLink) because the apply config types
// don't include them.
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

	result, err := json.Marshal(applyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal apply configuration: %w", err)
	}

	return result, nil
}
