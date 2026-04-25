package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
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
	return "Execute a shell command"
}

func (t *ShellTool) FullDescription() string {
	return "Execute a shell command in the current working directory and return its output. Output is truncated to last 1000 lines but the full output is saved to a temp file."
}

func (t *ShellTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"command"},
		Properties: map[string]llm.Property{
			"command": {
				Type:        "string",
				Description: "The shell command to execute",
			},
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
