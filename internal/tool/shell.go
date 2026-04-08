package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ShellTool executes shell commands.
type ShellTool struct{}

func NewShellTool() *ShellTool {
	return &ShellTool{}
}

func (t *ShellTool) Name() string {
	return "shell"
}

func (t *ShellTool) Description() string {
	return "Execute a shell command and return its output. Use this for running system commands, scripts, etc."
}

func (t *ShellTool) Parameters() map[string]any {
	return map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "The shell command to execute",
			"required":    true,
		},
	}
}

func (t *ShellTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return nil, fmt.Errorf("missing required parameter: command")
	}

	// Security: prevent dangerous commands
	dangerousPatterns := []string{
		"rm -rf", "mkfs", "dd if=", ":(){:|:&}", "/bin/sh -c", "/bin/bash -c",
	}
	for _, pattern := range dangerousPatterns {
		if strings.Contains(command, pattern) {
			return nil, fmt.Errorf("command rejected for security reasons")
		}
	}

	output, err := bash(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	return map[string]any{
		"output": output,
	}, nil
}

// bash executes a shell command and returns its output.
func bash(ctx context.Context, command string) (string, error) {
	workdir, hasWorkdir := FromWorkdir(ctx)

	cmd := exec.Command("bash", "-c", command)

	if hasWorkdir && workdir != "" {
		// Change to workdir before executing the command
		cmd.Dir = workdir
	} else {
		// Use current working directory
		cmd.Dir = "."
	}

	output, err := cmd.CombinedOutput()
	return string(output), err
}
