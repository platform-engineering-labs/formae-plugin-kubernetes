// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual shell gate with a minimal PATH and mock Pkl evaluation.
// Enumeration must never turn a failed command or empty result into success.
func TestEvalExamplesEnumeration(t *testing.T) {
	script, err := os.ReadFile("scripts/eval-examples.sh")
	if err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	for _, tc := range []struct {
		name, failedTool string
		empty, success   bool
	}{
		{name: "no-ripgrep", success: true},
		{name: "search-failed", failedTool: "grep"},
		{name: "project-search-failed", failedTool: "find"},
		{name: "no-examples", empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			scripts := filepath.Join(root, "scripts")
			examples := filepath.Join(root, "examples")
			for _, dir := range []string{bin, scripts, examples} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"dirname", "find", "sort", "grep"} {
				actual, err := exec.LookPath(tool)
				if err != nil {
					t.Skipf("%s is not installed", tool)
				}
				if tool == tc.failedTool {
					if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(actual, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			// No rg executable is provided, as on runners that only install Pkl.
			if err := os.WriteFile(filepath.Join(bin, "pkl"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "eval-examples.sh"), script, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(examples, "PklProject"), []byte("amends \"pkl:Project\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if !tc.empty {
				if err := os.WriteFile(filepath.Join(examples, "main.pkl"), []byte("forma {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(bash, filepath.Join(scripts, "eval-examples.sh"))
			cmd.Env = append(os.Environ(), "PATH="+bin)
			out, err := cmd.CombinedOutput()
			if tc.success {
				if err != nil || !strings.Contains(string(out), "1 forma checked, 0 failed") {
					t.Fatalf("expected one evaluated entry without rg: error=%v output=%s", err, out)
				}
			} else if err == nil {
				t.Fatalf("enumeration failure/empty result reported success: %s", out)
			}
		})
	}
}
