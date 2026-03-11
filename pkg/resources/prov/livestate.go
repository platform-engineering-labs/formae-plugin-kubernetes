// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"encoding/json"
	"fmt"
)

// FieldManager is the SSA field manager name used by all Formae operations.
const FieldManager = "formae"

// ExtractFunc is the signature of client-go SSA Extract functions.
// Example: appsv1ac.ExtractDeployment, v1coreac.ExtractConfigMap
type ExtractFunc[API any, AC any] func(*API, string) (*AC, error)

// ExtractState uses SSA Extract to return only fields owned by FieldManager as JSON.
// This returns exactly the fields Formae applied — no server defaults, no controller
// injected fields, no server-managed metadata.
func ExtractState[API any, AC any](apiObject *API, extractFn ExtractFunc[API, AC]) ([]byte, error) {
	extracted, err := extractFn(apiObject, FieldManager)
	if err != nil {
		return nil, fmt.Errorf("failed to extract field manager state: %w", err)
	}
	return json.Marshal(extracted)
}

// LiveState round-trips the full API object through the apply configuration type.
// This normalizes the structure (drops status, managedFields, etc. via omitempty)
// but retains ALL spec/metadata fields including server defaults.
// Use this to understand the full cluster state for drift detection.
func LiveState[T any](apiObject any) ([]byte, error) {
	data, err := json.Marshal(apiObject)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal API object: %w", err)
	}
	var ac T
	if err := json.Unmarshal(data, &ac); err != nil {
		return nil, fmt.Errorf("failed to unmarshal into apply configuration: %w", err)
	}
	return json.Marshal(ac)
}
