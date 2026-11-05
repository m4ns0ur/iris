//go:build unix

// Package proc centralises process-tree handling so that commands launched by
// tools (shell commands and approval hooks) can be terminated reliably.
package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// Configure places the command in its own process group. This makes it
// possible to signal the whole tree, not just the immediate child.
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Kill terminates the command's process group. It is safe to call after the
// process has already exited.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// Fall back to killing only the leader if the group is gone.
		return cmd.Process.Kill()
	}

	return nil
}
