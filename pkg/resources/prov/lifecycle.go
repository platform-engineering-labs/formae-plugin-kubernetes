// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"
	"encoding/json"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// LifecycleAware is a Provisioner decorator that detects K8S objects in
// terminating state (deletionTimestamp set). Without this check, objects
// blocked by finalizers appear alive to Formae sync and OOB deletes go
// undetected.
//
// Read/Status: if the returned properties contain metadata.deletionTimestamp,
// the object is dying — return NotFound so Formae treats it as deleted.
//
// Delete: after the inner Delete reports Success, verify the object is truly
// gone. If it still exists (finalizer-blocked), return InProgress so Formae
// polls via Status until the object is actually removed.
//
// Create, Update, List: pass through unchanged.
type LifecycleAware struct {
	Inner Provisioner
}

var _ Provisioner = &LifecycleAware{}

// Wrap returns a LifecycleAware decorator around the given Provisioner.
func Wrap(p Provisioner) Provisioner {
	return &LifecycleAware{Inner: p}
}

func (l *LifecycleAware) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	result, err := l.Inner.Create(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.ProgressResult != nil && len(result.ProgressResult.ResourceProperties) > 0 {
		result.ProgressResult.ResourceProperties = json.RawMessage(
			restoreEmptyObjects(string(result.ProgressResult.ResourceProperties), string(req.Properties)),
		)
	}
	return result, nil
}

func (l *LifecycleAware) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	result, err := l.Inner.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.ErrorCode != "" {
		return result, nil
	}
	if isTerminating([]byte(result.Properties)) {
		return &resource.ReadResult{
			ResourceType: result.ResourceType,
			ErrorCode:    resource.OperationErrorCodeNotFound,
		}, nil
	}
	result.Properties = stripDeletionTimestamp(result.Properties)
	return result, nil
}

func (l *LifecycleAware) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	result, err := l.Inner.Update(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.ProgressResult != nil && len(result.ProgressResult.ResourceProperties) > 0 {
		result.ProgressResult.ResourceProperties = json.RawMessage(
			restoreEmptyObjects(string(result.ProgressResult.ResourceProperties), string(req.DesiredProperties)),
		)
	}
	return result, nil
}

func (l *LifecycleAware) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	result, err := l.Inner.Delete(ctx, req)
	if err != nil {
		return nil, err
	}
	if !result.ProgressResult.FinishedSuccessfully() {
		return result, nil
	}

	// Verify the object is actually gone. K8S delete returns 200 even when
	// finalizers block removal — the object lingers with deletionTimestamp set.
	readResult, err := l.Inner.Read(ctx, &resource.ReadRequest{
		NativeID:     req.NativeID,
		ResourceType: req.ResourceType,
		TargetConfig: req.TargetConfig,
	})
	if err != nil {
		// Read failed — assume delete succeeded rather than blocking.
		return result, nil
	}
	if readResult.ErrorCode == resource.OperationErrorCodeNotFound {
		return result, nil
	}

	// Object still exists — but only report InProgress if it's actually
	// terminating (deletionTimestamp set, blocked by finalizers). Otherwise
	// the object may just not have been removed yet (race between delete
	// and read) and we should trust the original Success.
	if !isTerminating([]byte(readResult.Properties)) {
		return result, nil
	}

	return &resource.DeleteResult{
		ProgressResult: &resource.ProgressResult{
			Operation:       resource.OperationDelete,
			OperationStatus: resource.OperationStatusInProgress,
			NativeID:        req.NativeID,
			RequestID:       result.ProgressResult.RequestID,
			StatusMessage:   "waiting for resource deletion (finalizers pending)",
		},
	}, nil
}

func (l *LifecycleAware) Status(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	result, err := l.Inner.Status(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.ProgressResult.ErrorCode != "" {
		return result, nil
	}
	if isTerminating(result.ProgressResult.ResourceProperties) {
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		}, nil
	}
	result.ProgressResult.ResourceProperties = json.RawMessage(
		stripDeletionTimestamp(string(result.ProgressResult.ResourceProperties)),
	)
	return result, nil
}

func (l *LifecycleAware) List(ctx context.Context, req *resource.ListRequest) (*resource.ListResult, error) {
	return l.Inner.List(ctx, req)
}

// restoreEmptyObjects patches the result properties with empty objects ({}) that
// were present in the desired properties but stripped by SSA Extract. SSA treats
// empty objects as "no owned sub-fields" and omits them, but K8s semantics give
// meaning to empty objects (e.g. podSelector: {} means "select all pods").
func restoreEmptyObjects(result, desired string) string {
	if result == "" || desired == "" {
		return result
	}
	var resultMap, desiredMap map[string]any
	if err := json.Unmarshal([]byte(result), &resultMap); err != nil {
		return result
	}
	if err := json.Unmarshal([]byte(desired), &desiredMap); err != nil {
		return result
	}
	if injectEmptyObjects(resultMap, desiredMap) {
		patched, err := json.Marshal(resultMap)
		if err != nil {
			return result
		}
		return string(patched)
	}
	return result
}

// injectEmptyObjects recursively walks desired and injects empty objects into
// result where desired has {} but result is missing the key. Returns true if
// any injection occurred.
func injectEmptyObjects(result, desired map[string]any) bool {
	changed := false
	for key, desiredVal := range desired {
		desiredMap, isMap := desiredVal.(map[string]any)
		if !isMap {
			continue
		}
		resultVal, exists := result[key]
		if !exists {
			if len(desiredMap) == 0 {
				result[key] = map[string]any{}
				changed = true
			}
			continue
		}
		resultMap, isResultMap := resultVal.(map[string]any)
		if isResultMap {
			if injectEmptyObjects(resultMap, desiredMap) {
				changed = true
			}
		}
	}
	return changed
}

// isTerminating checks if the JSON properties contain metadata.deletionTimestamp.
func isTerminating(properties []byte) bool {
	if len(properties) == 0 {
		return false
	}
	var obj struct {
		Metadata struct {
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(properties, &obj); err != nil {
		return false
	}
	return obj.Metadata.DeletionTimestamp != nil
}

// stripDeletionTimestamp removes metadata.deletionTimestamp from properties
// so Formae doesn't see it as a user-managed field and trigger drift detection.
func stripDeletionTimestamp(properties string) string {
	if properties == "" {
		return properties
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(properties), &obj); err != nil {
		return properties
	}
	meta, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return properties
	}
	if _, exists := meta["deletionTimestamp"]; !exists {
		return properties
	}
	delete(meta, "deletionTimestamp")
	result, err := json.Marshal(obj)
	if err != nil {
		return properties
	}
	return string(result)
}
