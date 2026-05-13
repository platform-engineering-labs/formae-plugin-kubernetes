//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/config"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-k8s/pkg/transport"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestEnv holds the shared test infrastructure for integration tests.
type TestEnv struct {
	Client    *transport.Client
	Config    *config.Config
	Namespace string
	t         *testing.T
}

// SetupEnv creates a unique namespace and returns a TestEnv.
// The namespace is cleaned up automatically via t.Cleanup.
func SetupEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Sanitize test name for K8S namespace (lowercase, alphanumeric + hyphens, max 63 chars)
	name := strings.ToLower(t.Name())
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 40 {
		name = name[:40]
	}
	ns := fmt.Sprintf("int-%s-%d", name, time.Now().Unix()%100000)
	if len(ns) > 63 {
		ns = ns[:63]
	}

	targetJSON := `{"Auth": {"Type": "Kubeconfig", "Context": "orbstack"}}`
	cfg, err := config.FromTargetConfig([]byte(targetJSON))
	if err != nil {
		t.Fatalf("failed to parse target config: %v", err)
	}

	client, err := transport.NewClient(cfg)
	if err != nil {
		t.Fatalf("failed to create K8S client: %v", err)
	}

	// Create namespace
	ctx := context.Background()
	_, err = client.CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create namespace %s: %v", ns, err)
	}
	t.Logf("created namespace %s", ns)

	env := &TestEnv{
		Client:    client,
		Config:    cfg,
		Namespace: ns,
		t:         t,
	}

	t.Cleanup(func() { env.cleanup() })
	return env
}

func (e *TestEnv) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := e.Client.CoreV1().Namespaces().Delete(ctx, e.Namespace, metav1.DeleteOptions{})
	if err != nil {
		e.t.Logf("warning: failed to delete namespace %s: %v", e.Namespace, err)
	} else {
		e.t.Logf("deleted namespace %s", e.Namespace)
	}
}

// NewProvisioner creates a provisioner for the given resource type using the registry.
func (e *TestEnv) NewProvisioner(resourceType string) prov.Provisioner {
	e.t.Helper()
	factory, ok := registry.GetFactory(resourceType)
	if !ok {
		e.t.Fatalf("no factory registered for %s", resourceType)
	}
	return factory(e.Client, e.Config)
}

// MustMarshalJSON marshals v to JSON or fails the test.
func MustMarshalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return b
}

// WaitForStatus polls Status() until a terminal status is reached or timeout expires.
// Returns the final StatusResult.
func WaitForStatus(t *testing.T, p prov.Provisioner, nativeID, resourceType, requestID string, timeout time.Duration) *resource.StatusResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		result, err := p.Status(ctx, &resource.StatusRequest{
			NativeID:     nativeID,
			ResourceType: resourceType,
			RequestID:    requestID,
		})
		if err != nil {
			t.Fatalf("Status() error: %v", err)
		}

		if result.ProgressResult.HasFinished() {
			return result
		}
		t.Logf("status: %s (message: %s)", result.ProgressResult.OperationStatus, result.ProgressResult.StatusMessage)

		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for terminal status (last: %s)", result.ProgressResult.OperationStatus)
		case <-ticker.C:
		}
	}
}

// RequireSuccess asserts that a ProgressResult has OperationStatusSuccess.
func RequireSuccess(t *testing.T, pr *resource.ProgressResult, context string) {
	t.Helper()
	if pr.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("%s: expected Success, got %s (error: %s, message: %s)",
			context, pr.OperationStatus, pr.ErrorCode, pr.StatusMessage)
	}
}

// RequireNativeID asserts that a ProgressResult has a non-empty NativeID.
func RequireNativeID(t *testing.T, pr *resource.ProgressResult, context string) {
	t.Helper()
	if pr.NativeID == "" {
		t.Fatalf("%s: expected non-empty NativeID", context)
	}
}

// RequireProperties asserts that a ProgressResult has non-empty ResourceProperties.
func RequireProperties(t *testing.T, pr *resource.ProgressResult, context string) {
	t.Helper()
	if len(pr.ResourceProperties) == 0 {
		t.Fatalf("%s: expected non-empty ResourceProperties", context)
	}
}

// RequireReadProperties asserts that a ReadResult has non-empty Properties.
func RequireReadProperties(t *testing.T, rr *resource.ReadResult, context string) {
	t.Helper()
	if rr.Properties == "" {
		t.Fatalf("%s: expected non-empty Properties", context)
	}
}

// RequireNotFound asserts that a ReadResult has ErrorCode NotFound.
func RequireNotFound(t *testing.T, rr *resource.ReadResult, context string) {
	t.Helper()
	if rr.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Fatalf("%s: expected NotFound, got ErrorCode=%s Properties=%s",
			context, rr.ErrorCode, rr.Properties)
	}
}
