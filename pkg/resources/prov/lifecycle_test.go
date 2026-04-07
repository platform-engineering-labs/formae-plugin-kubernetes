// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package prov

import (
	"context"
	"encoding/json"
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
