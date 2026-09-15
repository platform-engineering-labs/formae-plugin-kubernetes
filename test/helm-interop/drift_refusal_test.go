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

func TestApplyExpectingRefusalRecognizesDriftDecisionPrompt(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "formae")
	const response = "Error: Reconcile needs drift decisions. Run apply in an interactive terminal to choose absorb or revert for every resource and review the final combined plan."
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+response+"' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &formaeCLI{t: t, binary: binary}
	state, message := f.ApplyExpectingRefusal("reconcile", "unused.pkl")
	if state != "Rejected" || !strings.Contains(message, "Reconcile needs drift decisions") {
		t.Fatalf("expected pre-submit drift refusal, got %q: %s", state, message)
	}
}
