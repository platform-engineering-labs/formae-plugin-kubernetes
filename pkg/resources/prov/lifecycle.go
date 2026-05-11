// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"

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
// Create, Update, List: pass through unchanged (other than panic recovery).
//
// All methods recover panics from the inner provisioner and convert them to
// a clean Failure result — the plugin runs as a .so loaded by the formae
// host, and an unrecovered panic crosses that boundary and crashes formae.
type LifecycleAware struct {
	Inner Provisioner
}

var _ Provisioner = &LifecycleAware{}

// Wrap returns a LifecycleAware decorator around the given Provisioner.
func Wrap(p Provisioner) Provisioner {
	return &LifecycleAware{Inner: p}
}

// recoverPanic converts a recovered panic into an error with a stack trace
// suitable for use as a Formae StatusMessage. Returns nil if r is nil.
func recoverPanic(r any, op resource.Operation) error {
	if r == nil {
		return nil
	}
	return fmt.Errorf("panic in %s: %v\n%s", op, r, debug.Stack())
}

func (l *LifecycleAware) Create(ctx context.Context, req *resource.CreateRequest) (result *resource.CreateResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationCreate)
			result = &resource.CreateResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationCreate,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeUnforeseenError,
					StatusMessage:   panicErr.Error(),
				},
			}
			err = nil
		}
	}()
	return l.Inner.Create(ctx, req)
}

func (l *LifecycleAware) Read(ctx context.Context, req *resource.ReadRequest) (result *resource.ReadResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationRead)
			rt := ""
			if req != nil {
				rt = req.ResourceType
			}
			result = &resource.ReadResult{
				ResourceType: rt,
				ErrorCode:    resource.OperationErrorCodeUnforeseenError,
			}
			err = panicErr
		}
	}()

	result, err = l.Inner.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
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

func (l *LifecycleAware) Update(ctx context.Context, req *resource.UpdateRequest) (result *resource.UpdateResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationUpdate)
			result = &resource.UpdateResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationUpdate,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeUnforeseenError,
					StatusMessage:   panicErr.Error(),
				},
			}
			err = nil
		}
	}()
	return l.Inner.Update(ctx, req)
}

func (l *LifecycleAware) Delete(ctx context.Context, req *resource.DeleteRequest) (result *resource.DeleteResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationDelete)
			result = &resource.DeleteResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationDelete,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeUnforeseenError,
					StatusMessage:   panicErr.Error(),
				},
			}
			err = nil
		}
	}()

	result, err = l.Inner.Delete(ctx, req)
	if err != nil {
		return nil, err
	}
	// Nil-guard: a misbehaving provisioner might return (nil, nil); treat that
	// as a no-op success rather than panicking through the .so boundary.
	if result == nil || result.ProgressResult == nil {
		return result, nil
	}
	if !result.ProgressResult.FinishedSuccessfully() {
		return result, nil
	}

	// Verify the object is actually gone. K8S delete returns 200 even when
	// finalizers block removal — the object lingers with deletionTimestamp set.
	readResult, readErr := l.Inner.Read(ctx, &resource.ReadRequest{
		NativeID:     req.NativeID,
		ResourceType: req.ResourceType,
		TargetConfig: req.TargetConfig,
	})
	if readErr != nil {
		// Propagate cancellation / deadline as errors so the orchestrator
		// can retry with fresh context rather than treating the resource as
		// successfully deleted.
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, readErr
		}
		// Transient 5xx/throttle/etc. — don't claim success. Report
		// InProgress so Formae polls Status until the picture clears.
		return &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusInProgress,
				NativeID:        req.NativeID,
				RequestID:       result.ProgressResult.RequestID,
				StatusMessage:   fmt.Sprintf("post-delete read failed: %v; treating as in-progress", readErr),
			},
		}, nil
	}
	if readResult == nil || readResult.ErrorCode == resource.OperationErrorCodeNotFound {
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

func (l *LifecycleAware) Status(ctx context.Context, req *resource.StatusRequest) (result *resource.StatusResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationCheckStatus)
			result = &resource.StatusResult{
				ProgressResult: &resource.ProgressResult{
					Operation:       resource.OperationCheckStatus,
					OperationStatus: resource.OperationStatusFailure,
					ErrorCode:       resource.OperationErrorCodeUnforeseenError,
					StatusMessage:   panicErr.Error(),
				},
			}
			err = nil
		}
	}()

	result, err = l.Inner.Status(ctx, req)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ProgressResult == nil {
		return result, nil
	}
	if result.ProgressResult.ErrorCode != "" {
		return result, nil
	}
	if isTerminating(result.ProgressResult.ResourceProperties) {
		// Preserve RequestID from the input request and NativeID from the
		// inner result so Formae's orchestrator can correlate this status
		// callback with the original operation.
		nativeID := result.ProgressResult.NativeID
		requestID := ""
		if req != nil {
			requestID = req.RequestID
		}
		return &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
				NativeID:        nativeID,
				RequestID:       requestID,
				StatusMessage:   "resource is terminating",
			},
		}, nil
	}
	// Don't rewrite empty properties — writing back json.RawMessage("")
	// produces invalid JSON downstream.
	if len(result.ProgressResult.ResourceProperties) > 0 {
		result.ProgressResult.ResourceProperties = json.RawMessage(
			stripDeletionTimestamp(string(result.ProgressResult.ResourceProperties)),
		)
	}
	return result, nil
}

func (l *LifecycleAware) List(ctx context.Context, req *resource.ListRequest) (result *resource.ListResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicErr := recoverPanic(r, resource.OperationList)
			result = nil
			err = panicErr
		}
	}()
	return l.Inner.List(ctx, req)
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
