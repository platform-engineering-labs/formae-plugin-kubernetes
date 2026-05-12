// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package prov

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvisioner implements Provisioner for testing the lifecycle decorator.
type mockProvisioner struct {
	readResult   *resource.ReadResult
	readErr      error
	deleteResult *resource.DeleteResult
	deleteErr    error
	statusResult *resource.StatusResult
	statusErr    error
}

func (m *mockProvisioner) Create(_ context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	return nil, nil
}

func (m *mockProvisioner) Read(_ context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	return m.readResult, m.readErr
}

func (m *mockProvisioner) Update(_ context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	return nil, nil
}

func (m *mockProvisioner) Delete(_ context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	return m.deleteResult, m.deleteErr
}

func (m *mockProvisioner) Status(_ context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	return m.statusResult, m.statusErr
}

func (m *mockProvisioner) List(_ context.Context, req *resource.ListRequest) (*resource.ListResult, error) {
	return nil, nil
}

// --- isTerminating tests ---

func TestIsTerminating_WithDeletionTimestamp(t *testing.T) {
	props := `{"metadata":{"name":"test","deletionTimestamp":"2026-01-01T00:00:00Z"}}`
	assert.True(t, isTerminating([]byte(props)), "should be terminating when deletionTimestamp is set")
}

func TestIsTerminating_WithoutDeletionTimestamp(t *testing.T) {
	props := `{"metadata":{"name":"test"}}`
	assert.False(t, isTerminating([]byte(props)), "should not be terminating when deletionTimestamp is absent")
}

func TestIsTerminating_EmptyInput(t *testing.T) {
	assert.False(t, isTerminating(nil), "nil input")
	assert.False(t, isTerminating([]byte{}), "empty input")
}

func TestIsTerminating_MalformedJSON(t *testing.T) {
	assert.False(t, isTerminating([]byte("not json")))
}

func TestIsTerminating_NoMetadata(t *testing.T) {
	assert.False(t, isTerminating([]byte(`{"kind":"ConfigMap"}`)))
}

// --- stripDeletionTimestamp tests ---

func TestStripDeletionTimestamp_RemovesField(t *testing.T) {
	input := `{"metadata":{"name":"test","deletionTimestamp":"2026-01-01T00:00:00Z"}}`
	result := stripDeletionTimestamp(input)

	var obj map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result), &obj))
	meta := obj["metadata"].(map[string]interface{})
	assert.NotContains(t, meta, "deletionTimestamp")
	assert.Equal(t, "test", meta["name"])
}

func TestStripDeletionTimestamp_NoDeletionTimestamp(t *testing.T) {
	input := `{"metadata":{"name":"test"}}`
	assert.Equal(t, input, stripDeletionTimestamp(input))
}

func TestStripDeletionTimestamp_EmptyString(t *testing.T) {
	assert.Equal(t, "", stripDeletionTimestamp(""))
}

// --- LifecycleAware.Read tests ---

func TestRead_PassthroughNotFound(t *testing.T) {
	inner := &mockProvisioner{
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			ErrorCode:    resource.OperationErrorCodeNotFound,
		},
	}
	la := Wrap(inner)
	result, err := la.Read(context.Background(), &resource.ReadRequest{
		ResourceType: "K8S::Core::ConfigMap",
		NativeID:     "default/test",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
}

func TestRead_ReturnsNotFoundWhenTerminating(t *testing.T) {
	inner := &mockProvisioner{
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			Properties:   `{"metadata":{"name":"test","deletionTimestamp":"2026-01-01T00:00:00Z"}}`,
		},
	}
	la := Wrap(inner)
	result, err := la.Read(context.Background(), &resource.ReadRequest{
		ResourceType: "K8S::Core::ConfigMap",
		NativeID:     "default/test",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ErrorCode)
}

func TestRead_StripsDeletionTimestampFromLiveObject(t *testing.T) {
	inner := &mockProvisioner{
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			Properties:   `{"metadata":{"name":"test","namespace":"default"}}`,
		},
	}
	la := Wrap(inner)
	result, err := la.Read(context.Background(), &resource.ReadRequest{
		ResourceType: "K8S::Core::ConfigMap",
		NativeID:     "default/test",
	})
	require.NoError(t, err)
	assert.Empty(t, result.ErrorCode)
	assert.NotEmpty(t, result.Properties)
}

// --- LifecycleAware.Delete tests ---

func TestDelete_SuccessWhenObjectGone(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
			},
		},
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			ErrorCode:    resource.OperationErrorCodeNotFound,
		},
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestDelete_InProgressWhenObjectLingers(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
			},
		},
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			Properties:   `{"metadata":{"name":"test","deletionTimestamp":"2026-01-01T00:00:00Z"}}`,
		},
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
}

func TestDelete_SuccessWhenObjectExistsWithoutDeletionTimestamp(t *testing.T) {
	// Race condition: object still exists briefly after delete but has no
	// deletionTimestamp — not finalizer-blocked, just a timing race.
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
			},
		},
		readResult: &resource.ReadResult{
			ResourceType: "K8S::Core::ConfigMap",
			Properties:   `{"metadata":{"name":"test","namespace":"default"}}`,
		},
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusSuccess, result.ProgressResult.OperationStatus)
}

func TestDelete_PassthroughOnFailure(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusFailure,
			},
		},
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
}

// --- LifecycleAware.Status tests ---

func TestStatus_ReturnsNotFoundWhenTerminating(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:          resource.OperationCheckStatus,
				OperationStatus:    resource.OperationStatusSuccess,
				ResourceProperties: json.RawMessage(`{"metadata":{"name":"test","deletionTimestamp":"2026-01-01T00:00:00Z"}}`),
			},
		},
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ProgressResult.ErrorCode)
}

func TestStatus_PassthroughNotFound(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationCheckStatus,
				OperationStatus: resource.OperationStatusFailure,
				ErrorCode:       resource.OperationErrorCodeNotFound,
			},
		},
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ProgressResult.ErrorCode)
}

// --- nil-guard tests (C-LC-1) ---

func TestDelete_NilResultDoesNotPanic(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: nil,
		deleteErr:    nil,
	}
	la := Wrap(inner)
	// Should not panic; just return whatever the inner did (nil, nil).
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestDelete_NilProgressResultDoesNotPanic(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{},
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	// Nil ProgressResult is preserved — caller can interpret.
	assert.Nil(t, result.ProgressResult)
}

func TestStatus_NilResultDoesNotPanic(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: nil,
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestStatus_NilProgressResultDoesNotPanic(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: &resource.StatusResult{},
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Nil(t, result.ProgressResult)
}

// --- panic-recovery tests (C-LC-1) ---

type panickyProvisioner struct{}

func (p *panickyProvisioner) Create(_ context.Context, _ *resource.CreateRequest) (*resource.CreateResult, error) {
	panic("kaboom-create")
}
func (p *panickyProvisioner) Read(_ context.Context, _ *resource.ReadRequest) (*resource.ReadResult, error) {
	panic("kaboom-read")
}
func (p *panickyProvisioner) Update(_ context.Context, _ *resource.UpdateRequest) (*resource.UpdateResult, error) {
	panic("kaboom-update")
}
func (p *panickyProvisioner) Delete(_ context.Context, _ *resource.DeleteRequest) (*resource.DeleteResult, error) {
	panic("kaboom-delete")
}
func (p *panickyProvisioner) Status(_ context.Context, _ *resource.StatusRequest) (*resource.StatusResult, error) {
	panic("kaboom-status")
}
func (p *panickyProvisioner) List(_ context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	panic("kaboom-list")
}

func TestCreate_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.Create(context.Background(), &resource.CreateRequest{ResourceType: "K8S::Core::ConfigMap"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationStatusFailure, result.ProgressResult.OperationStatus)
	assert.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
	assert.Contains(t, result.ProgressResult.StatusMessage, "kaboom-create")
	assert.Contains(t, result.ProgressResult.StatusMessage, "goroutine ")
}

func TestUpdate_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.Update(context.Background(), &resource.UpdateRequest{ResourceType: "K8S::Core::ConfigMap"})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
}

func TestDelete_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{ResourceType: "K8S::Core::ConfigMap", NativeID: "default/x"})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
	assert.Contains(t, result.ProgressResult.StatusMessage, "kaboom-delete")
}

func TestStatus_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.Status(context.Background(), &resource.StatusRequest{ResourceType: "K8S::Core::ConfigMap"})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ProgressResult.ErrorCode)
}

func TestRead_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.Read(context.Background(), &resource.ReadRequest{ResourceType: "K8S::Core::ConfigMap"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, resource.OperationErrorCodeUnforeseenError, result.ErrorCode)
	assert.True(t, strings.Contains(err.Error(), "kaboom-read"))
}

func TestList_PanicRecovered(t *testing.T) {
	la := Wrap(&panickyProvisioner{})
	result, err := la.List(context.Background(), &resource.ListRequest{ResourceType: "K8S::Core::ConfigMap"})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "kaboom-list"))
}

// --- C-LC-3: Delete swallows Read error ---

func TestDelete_TransientReadError_ReturnsInProgress(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
				RequestID:       "req-1",
			},
		},
		readErr: errors.New("503 service unavailable"),
	}
	la := Wrap(inner)
	result, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, resource.OperationStatusInProgress, result.ProgressResult.OperationStatus)
	// InProgress must not carry a StatusMessage — that surfaces in
	// Formae's per-resource line as a `reason` row and should be reserved
	// for actual failures.
	assert.Empty(t, result.ProgressResult.StatusMessage)
	assert.Equal(t, "req-1", result.ProgressResult.RequestID)
}

func TestDelete_DeadlineExceeded_PropagatesError(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
			},
		},
		readErr: context.DeadlineExceeded,
	}
	la := Wrap(inner)
	_, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestDelete_CanceledContext_PropagatesError(t *testing.T) {
	inner := &mockProvisioner{
		deleteResult: &resource.DeleteResult{
			ProgressResult: &resource.ProgressResult{
				Operation:       resource.OperationDelete,
				OperationStatus: resource.OperationStatusSuccess,
				NativeID:        "default/test",
			},
		},
		readErr: context.Canceled,
	}
	la := Wrap(inner)
	_, err := la.Delete(context.Background(), &resource.DeleteRequest{
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// --- C-LC-4: Status doesn't rewrite empty ResourceProperties ---

func TestStatus_EmptyResourcePropertiesNotRewritten(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:          resource.OperationCheckStatus,
				OperationStatus:    resource.OperationStatusSuccess,
				ResourceProperties: nil,
				NativeID:           "default/test",
			},
		},
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		RequestID:    "rq-9",
		NativeID:     "default/test",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	// Crucially: no `""` rewrite happened.
	assert.Empty(t, result.ProgressResult.ResourceProperties)
}

// --- C-LC-5: Status terminating preserves RequestID + NativeID ---

func TestStatus_TerminatingPreservesRequestIDAndNativeID(t *testing.T) {
	inner := &mockProvisioner{
		statusResult: &resource.StatusResult{
			ProgressResult: &resource.ProgressResult{
				Operation:          resource.OperationCheckStatus,
				OperationStatus:    resource.OperationStatusSuccess,
				NativeID:           "default/dying",
				ResourceProperties: json.RawMessage(`{"metadata":{"name":"dying","deletionTimestamp":"2026-01-01T00:00:00Z"}}`),
			},
		},
	}
	la := Wrap(inner)
	result, err := la.Status(context.Background(), &resource.StatusRequest{
		RequestID:    "req-42",
		NativeID:     "default/dying",
		ResourceType: "K8S::Core::ConfigMap",
	})
	require.NoError(t, err)
	require.NotNil(t, result.ProgressResult)
	assert.Equal(t, "req-42", result.ProgressResult.RequestID)
	assert.Equal(t, "default/dying", result.ProgressResult.NativeID)
	assert.Equal(t, resource.OperationErrorCodeNotFound, result.ProgressResult.ErrorCode)
	// Retryable terminating state — no StatusMessage so Formae's
	// per-resource line doesn't display a `reason` row mid-retry.
	assert.Empty(t, result.ProgressResult.StatusMessage)
}
