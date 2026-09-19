//go:build integration

// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelmInteropApplyRecognizesDriftDecisionPrompt(t *testing.T) {
	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "formae")
	calls := filepath.Join(tempDir, "calls")
	t.Setenv("FAKE_FORMAE_CALLS", calls)
	err := os.WriteFile(binary, []byte(`#!/bin/sh
printf x >> "$FAKE_FORMAE_CALLS"
echo "Error: Reconcile needs drift decisions. Run apply in an interactive terminal to choose absorb or revert for every resource and review the final combined plan." >&2
exit 1
`), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	cli := &formaeCLI{t: t, binary: binary}
	state, message := cli.ApplyExpectingRefusal("reconcile", "fixture.pkl")
	if state != "DecisionRequired" {
		t.Fatalf("state = %q, want DecisionRequired", state)
	}
	if !strings.Contains(message, "Reconcile needs drift decisions") {
		t.Fatalf("message = %q, want drift-decision prompt", message)
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if string(invocations) != "x" {
		t.Fatalf("CLI invocations = %q, want one", invocations)
	}
}
