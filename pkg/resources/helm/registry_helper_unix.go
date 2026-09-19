//go:build linux || darwin

// © 2026 Platform Engineering Labs Inc.
// SPDX-License-Identifier: Apache-2.0

package helm

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Each helper owns a fresh process group; cancellation never targets a shared
// terminal/agent group. This also closes pipes inherited by ordinary children.
func configureRegistryHelper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
func stopRegistryHelper(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
