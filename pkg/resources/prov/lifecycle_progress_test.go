// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package prov

import (
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

func TestClearStatusMessage_PreservedOnInProgress(t *testing.T) {
	pr := &resource.ProgressResult{OperationStatus: resource.OperationStatusInProgress, StatusMessage: "replicas: 2/3 ready"}
	clearStatusMessageUnlessFailure(pr)
	if pr.StatusMessage != "replicas: 2/3 ready" {
		t.Fatalf("expected progress message preserved on InProgress, got %q", pr.StatusMessage)
	}
}

func TestClearStatusMessage_BlankedOnSuccess(t *testing.T) {
	pr := &resource.ProgressResult{OperationStatus: resource.OperationStatusSuccess, StatusMessage: "replicas: 3/3 ready"}
	clearStatusMessageUnlessFailure(pr)
	if pr.StatusMessage != "" {
		t.Fatalf("expected message blanked on Success, got %q", pr.StatusMessage)
	}
}

func TestClearStatusMessage_PreservedOnFailure(t *testing.T) {
	pr := &resource.ProgressResult{OperationStatus: resource.OperationStatusFailure, StatusMessage: "boom"}
	clearStatusMessageUnlessFailure(pr)
	if pr.StatusMessage != "boom" {
		t.Fatalf("expected failure message preserved, got %q", pr.StatusMessage)
	}
}
