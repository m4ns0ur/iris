package tool

import (
	"os/exec"
)

// bash executes a shell command and returns its output.
func bash(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
