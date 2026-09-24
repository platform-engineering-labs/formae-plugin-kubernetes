//go:build !linux && !darwin

// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import "os/exec"

// CommandContext terminates the direct helper; WaitDelay bounds inherited-pipe
// waits. Descendant-group termination is only guaranteed on Linux and macOS.
func configureRegistryHelper(*exec.Cmd) {}
func stopRegistryHelper(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
