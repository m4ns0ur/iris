package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	return "Run a shell command"
}

func (t *ShellTool) FullDescription() string {
	return "Run a shell command in the current directory. Return its output (last 1000 lines or 25KB; full output saved to a temp file)."
}

func (t *ShellTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"command"},
		Properties: map[string]llm.Property{
			"command": {
				Type:        "string",
				Description: "Shell command",
			},
		},
	}
}

func (t *ShellTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return nil, fmt.Errorf("missing required parameter: command")
	}

	dangerousPatterns := []string{
		"rm -rf", "mkfs", "dd if=", ":(){:|:&}", "/bin/sh -c", "/bin/bash -c",
	}
	for _, pattern := range dangerousPatterns {
		if strings.Contains(command, pattern) {
			return nil, fmt.Errorf("command rejected for security reasons")
		}
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	workdir, hasWorkdir := FromWorkdir(ctx)
	if hasWorkdir && workdir != "" {
		cmd.Dir = workdir
	} else {
		cmd.Dir = "."
	}

	fullOutput, err := cmd.CombinedOutput()

	tempFile, fileErr := os.CreateTemp("", "shell-output-*.log")
	if fileErr == nil {
		_, _ = tempFile.Write(fullOutput)
		_ = tempFile.Close()
	}

	const maxBytes = 25 * 1024
	out := fullOutput

	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}

	lines := bytes.Split(out, []byte("\n"))
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
		out = bytes.Join(lines, []byte("\n"))
	}

	return map[string]any{
		"output": string(out),
		"file":   tempFile.Name(),
	}, err
}
