//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// ResourceFixture defines the test data and expectations for a resource CRUD lifecycle.
type ResourceFixture struct {
	// ResourceType is the 3-segment type (e.g. "K8S::Core::Pod").
	ResourceType string

	// IsNamespaced indicates if the resource lives in a namespace.
	IsNamespaced bool

	// CreateProperties returns the JSON properties for Create.
	// The namespace argument is the test namespace (empty for cluster-scoped).
	CreateProperties func(ns string) json.RawMessage

	// UpdateProperties returns the JSON properties for Update.
	// Nil if SkipUpdate is true.
	UpdateProperties func(ns string) json.RawMessage

	// ExpectedCreateStatus is the expected OperationStatus from Create.
	ExpectedCreateStatus resource.OperationStatus

	// ExpectedFinalStatus is the expected terminal status after polling.
	ExpectedFinalStatus resource.OperationStatus

	// StatusTimeout is how long to poll Status() before giving up.
	StatusTimeout time.Duration

	// SkipUpdate skips the update step (for immutable resources like Jobs).
	SkipUpdate bool

	// VerifyCreate is an optional callback to verify the Create result.
	VerifyCreate func(t *testing.T, result *resource.CreateResult)

	// VerifyRead is an optional callback to verify the Read result.
	VerifyRead func(t *testing.T, result *resource.ReadResult)

	// VerifyUpdate is an optional callback to verify the Update result.
	VerifyUpdate func(t *testing.T, result *resource.UpdateResult)

	// VerifyList is an optional callback to verify the List result.
	VerifyList func(t *testing.T, result *resource.ListResult, nativeID string)

	// Setup runs after environment creation but before the CRUD lifecycle.
	// Use this to create prerequisite resources (e.g., Deployment for HPA).
	Setup func(t *testing.T, env *TestEnv)

	// CleanupExtra runs additional cleanup for cluster-scoped resources.
	CleanupExtra func(t *testing.T, env *TestEnv, nativeID string)
}

// RunCRUDLifecycle runs an 8-step CRUD lifecycle test for the given fixture.
//
//  1. Create - create the resource and verify NativeID + properties
//  2. Read - verify properties can be read back
//  3. Status - poll until terminal status
//  4. Update - apply updated properties (skipped if SkipUpdate)
//  5. Status - poll after update (skipped if SkipUpdate)
//  6. List - verify NativeID appears in list results
//  7. Delete - delete the resource
//  8. Read - verify resource is gone (NotFound)
func RunCRUDLifecycle(t *testing.T, fixture ResourceFixture) {
	t.Helper()

	env := SetupEnv(t)
	p := env.NewProvisioner(fixture.ResourceType)

	if fixture.Setup != nil {
		fixture.Setup(t, env)
	}

	// Always pass namespace name — namespaced resources use it in metadata.namespace,
	// cluster-scoped resources use it for unique naming.
	ns := env.Namespace

	ctx := context.Background()
	var nativeID string
	var requestID string

	// Step 1: Create
	t.Log("step 1: Create")
	createResult, err := p.Create(ctx, &resource.CreateRequest{
		ResourceType: fixture.ResourceType,
		Properties:   fixture.CreateProperties(ns),
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	pr := createResult.ProgressResult
	RequireNativeID(t, pr, "Create")
	RequireProperties(t, pr, "Create")
	if pr.OperationStatus != fixture.ExpectedCreateStatus {
		t.Fatalf("Create: expected status %s, got %s", fixture.ExpectedCreateStatus, pr.OperationStatus)
	}
	nativeID = pr.NativeID
	requestID = pr.RequestID
	t.Logf("created %s (nativeID=%s, status=%s)", fixture.ResourceType, nativeID, pr.OperationStatus)

	if fixture.VerifyCreate != nil {
		fixture.VerifyCreate(t, createResult)
	}

	// Register extra cleanup now that we have nativeID
	if fixture.CleanupExtra != nil {
		nid := nativeID // capture
		t.Cleanup(func() {
			fixture.CleanupExtra(t, env, nid)
		})
	}

	// Step 2: Read
	t.Log("step 2: Read")
	readResult, err := p.Read(ctx, &resource.ReadRequest{
		NativeID:     nativeID,
		ResourceType: fixture.ResourceType,
	})
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	RequireReadProperties(t, readResult, "Read")
	t.Logf("read OK (properties length=%d)", len(readResult.Properties))

	if fixture.VerifyRead != nil {
		fixture.VerifyRead(t, readResult)
	}

	// Step 3: Status (poll until terminal)
	t.Log("step 3: Status (polling)")
	statusResult := WaitForStatus(t, p, nativeID, fixture.ResourceType, requestID, fixture.StatusTimeout)
	if statusResult.ProgressResult.OperationStatus != fixture.ExpectedFinalStatus {
		t.Fatalf("Status: expected %s, got %s (message: %s)",
			fixture.ExpectedFinalStatus, statusResult.ProgressResult.OperationStatus, statusResult.ProgressResult.StatusMessage)
	}
	t.Logf("status reached %s", statusResult.ProgressResult.OperationStatus)

	// Step 4: Update (optional)
	if !fixture.SkipUpdate {
		t.Log("step 4: Update")
		updateResult, err := p.Update(ctx, &resource.UpdateRequest{
			NativeID:          nativeID,
			ResourceType:      fixture.ResourceType,
			DesiredProperties: fixture.UpdateProperties(ns),
		})
		if err != nil {
			t.Fatalf("Update() error: %v", err)
		}
		RequireNativeID(t, updateResult.ProgressResult, "Update")
		RequireProperties(t, updateResult.ProgressResult, "Update")
		requestID = updateResult.ProgressResult.RequestID
		t.Logf("updated (status=%s)", updateResult.ProgressResult.OperationStatus)

		if fixture.VerifyUpdate != nil {
			fixture.VerifyUpdate(t, updateResult)
		}

		// Step 5: Status after update
		t.Log("step 5: Status after update (polling)")
		statusResult = WaitForStatus(t, p, nativeID, fixture.ResourceType, requestID, fixture.StatusTimeout)
		if statusResult.ProgressResult.OperationStatus != fixture.ExpectedFinalStatus {
			t.Fatalf("Status after update: expected %s, got %s",
				fixture.ExpectedFinalStatus, statusResult.ProgressResult.OperationStatus)
		}
		t.Logf("status after update reached %s", statusResult.ProgressResult.OperationStatus)
	} else {
		t.Log("step 4: Update (skipped)")
		t.Log("step 5: Status after update (skipped)")
	}

	// Step 6: List
	t.Log("step 6: List")
	listReq := &resource.ListRequest{
		ResourceType:         fixture.ResourceType,
		AdditionalProperties: map[string]string{},
	}
	if fixture.IsNamespaced {
		listReq.AdditionalProperties["namespace"] = env.Namespace
	}
	listResult, err := p.List(ctx, listReq)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	found := false
	for _, id := range listResult.NativeIDs {
		if id == nativeID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List: nativeID %s not found in results (%d items)", nativeID, len(listResult.NativeIDs))
	}
	t.Logf("list OK (%d items, found target)", len(listResult.NativeIDs))

	if fixture.VerifyList != nil {
		fixture.VerifyList(t, listResult, nativeID)
	}

	// Step 7: Delete
	t.Log("step 7: Delete")
	deleteResult, err := p.Delete(ctx, &resource.DeleteRequest{
		NativeID:     nativeID,
		ResourceType: fixture.ResourceType,
	})
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	RequireSuccess(t, deleteResult.ProgressResult, "Delete")
	t.Log("deleted OK")

	// Step 8: Read after delete (poll until NotFound)
	t.Log("step 8: Read after delete (polling for NotFound)")
	deadline := time.Now().Add(30 * time.Second)
	for {
		readResult, err = p.Read(ctx, &resource.ReadRequest{
			NativeID:     nativeID,
			ResourceType: fixture.ResourceType,
		})
		if err != nil {
			t.Fatalf("Read() after delete error: %v", err)
		}
		if readResult.ErrorCode == resource.OperationErrorCodeNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Read after delete: still found resource after 30s (ErrorCode=%s)", readResult.ErrorCode)
		}
		time.Sleep(2 * time.Second)
	}
	t.Log("confirmed NotFound after delete")
}

// containsKey checks if a JSON object contains a given key at the top level.
func ContainsKey(t *testing.T, jsonStr string, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		t.Fatalf("failed to unmarshal JSON for key check: %v", err)
	}
	_, ok := m[key]
	return ok
}

// JSONPath extracts a string value from a JSON string using a simple dot-separated path.
// Returns empty string if not found.
func JSONPath(t *testing.T, jsonStr string, path ...string) string {
	t.Helper()
	var current any
	if err := json.Unmarshal([]byte(jsonStr), &current); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = m[key]
		if !ok {
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

