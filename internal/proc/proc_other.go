//go:build !unix

package proc

import "os/exec"

// Configure is a no-op on platforms without Unix process groups.
func Configure(cmd *exec.Cmd) {}

// Kill terminates the direct child process.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
