//go:build integration

// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// A version-override target config pins the K8s version, so the gate fires
// without any live cluster call (only a kubeconfig is needed to build the
// client). MutatingAdmissionPolicy (introducedIn 1.36) is unsupported on 1.33.
const (
	gatedType  = "K8S::Admissionregistration::MutatingAdmissionPolicy"
	gateTarget = `{"Auth":{"Type":"Kubeconfig","Context":"orbstack"},"KubernetesVersion":"1.33"}`
)

func TestTypeGate_UnsupportedType(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()
	tc := []byte(gateTarget)

	t.Run("List returns empty", func(t *testing.T) {
		res, err := p.List(ctx, &resource.ListRequest{ResourceType: gatedType, TargetConfig: tc})
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(res.NativeIDs) != 0 {
			t.Fatalf("expected empty List for gated type, got %d", len(res.NativeIDs))
		}
	})

	t.Run("Create errors with version reason", func(t *testing.T) {
		_, err := p.Create(ctx, &resource.CreateRequest{ResourceType: gatedType, TargetConfig: tc,
			Properties: []byte(`{"apiVersion":"admissionregistration.k8s.io/v1","kind":"MutatingAdmissionPolicy","metadata":{"name":"x"}}`)})
		if err == nil || !strings.Contains(err.Error(), "1.36") {
			t.Fatalf("expected version error mentioning 1.36, got: %v", err)
		}
	})

	t.Run("Read returns NotFound", func(t *testing.T) {
		res, err := p.Read(ctx, &resource.ReadRequest{ResourceType: gatedType, TargetConfig: tc, NativeID: "x"})
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		if res.ErrorCode != resource.OperationErrorCodeNotFound {
			t.Fatalf("expected NotFound, got %q", res.ErrorCode)
		}
	})

	t.Run("Delete is a success no-op", func(t *testing.T) {
		res, err := p.Delete(ctx, &resource.DeleteRequest{ResourceType: gatedType, TargetConfig: tc, NativeID: "x"})
		if err != nil {
			t.Fatalf("Delete error: %v", err)
		}
		if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
			t.Fatalf("expected success no-op delete, got %q", res.ProgressResult.OperationStatus)
		}
	})
}

// Live path: no version override, so ResolveVersion calls the cluster's
// ServerVersion(). orbstack reports ~1.33, so MutatingAdmissionPolicy is gated
// and List returns empty with no error — the real discovery-spam fix.
func TestTypeGate_LiveVersionResolution(t *testing.T) {
	p := &Plugin{}
	res, err := p.List(context.Background(), &resource.ListRequest{
		ResourceType: gatedType,
		TargetConfig: []byte(`{"Auth":{"Type":"Kubeconfig","Context":"orbstack"}}`),
	})
	if err != nil {
		t.Fatalf("List error (expected empty, no error): %v", err)
	}
	if len(res.NativeIDs) != 0 {
		t.Fatalf("expected empty List, got %d", len(res.NativeIDs))
	}
}
