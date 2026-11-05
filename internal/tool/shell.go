package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/internal/proc"
)

// ShellTool executes shell commands under the configured execution policy.
type ShellTool struct {
	policy *policy.Policy
	temp   *TempStore
}

func NewShellTool(pol *policy.Policy, temp *TempStore) *ShellTool {
	return &ShellTool{policy: pol, temp: temp}
}

func (t *ShellTool) Name() string {
	return "shell"
}

func (t *ShellTool) Description() string {
	return "Run a shell command"
}

func (t *ShellTool) FullDescription() string {
	if t.policy == nil {
		return "Run a shell command in the workspace. Commands are subject to the security policy."
	}

	options := t.policy.ShellOptions()
	return fmt.Sprintf(
		"Run a shell command in the workspace. Commands are subject to the security policy: deny rules, allow rules, and user approval. A %s timeout terminates the whole process group. Output is bounded (last %d lines or %d bytes); truncated output is saved to a temp file.",
		options.Timeout,
		options.MaxOutputLines,
		options.MaxOutputBytes,
	)
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
	if !ok || strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("missing required parameter: command")
	}

	if t.policy == nil {
		return nil, fmt.Errorf("%w: shell tool has no security policy", ErrExecutionFailed)
	}

	workdir := t.policy.Workdir()

	if _, err := t.policy.AuthorizeShell(ctx, command, workdir); err != nil {
		return nil, err
	}

	shellOptions := t.policy.ShellOptions()
	shellBinary := shellOptions.ShellBinary
	if shellBinary == "" {
		shellBinary = "bash"
	}

	runCtx, cancel := context.WithTimeout(ctx, shellOptions.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, shellBinary, "-c", command)
	cmd.Dir = workdir
	proc.Configure(cmd)
	cmd.Cancel = func() error { return proc.Kill(cmd) }
	cmd.WaitDelay = 5 * time.Second

	var outputFile *os.File
	var outputPath string

	if t.temp != nil {
		if file, err := t.temp.NewFile("shell-*.log"); err == nil {
			outputFile = file
			outputPath = file.Name()
		}
	}

	capture := newCaptureWriter(
		shellOptions.MaxOutputBytes,
		outputFile,
		shellOptions.MaxCaptureBytes,
	)
	cmd.Stdout = capture
	cmd.Stderr = capture

	startedAt := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(startedAt)

	if outputFile != nil {
		_ = outputFile.Close()
	}

	snapshot := capture.Snapshot()

	if runErr != nil && cmd.ProcessState == nil {
		if outputPath != "" && t.temp != nil {
			t.temp.Remove(outputPath)
		}
		return nil, fmt.Errorf("failed to run command: %w", runErr)
	}

	output, lineTruncated := limitOutput(
		snapshot.Tail,
		shellOptions.MaxOutputBytes,
		shellOptions.MaxOutputLines,
	)
	truncated := lineTruncated || snapshot.Total > int64(len(snapshot.Tail))

	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	canceled := errors.Is(runCtx.Err(), context.Canceled)

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	result := map[string]any{
		"output":      output,
		"exit_code":   exitCode,
		"success":     runErr == nil && !timedOut && !canceled,
		"truncated":   truncated,
		"duration_ms": elapsed.Milliseconds(),
	}

	if timedOut {
		result["timed_out"] = true
		result["error"] = fmt.Sprintf(
			"command exceeded the %s timeout and its process group was terminated",
			shellOptions.Timeout,
		)
	}
	if canceled {
		result["canceled"] = true
		result["error"] = "command was canceled"
	}

	if runErr != nil && !timedOut && !canceled {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			result["error"] = runErr.Error()
		}
	}

	if truncated && outputPath != "" && snapshot.FileErr == nil {
		result["full_output_file"] = outputPath
		result["full_output_truncated"] = snapshot.FileTruncated
		result["full_output_bytes"] = snapshot.FileWritten
	} else if outputPath != "" && t.temp != nil {
		t.temp.Remove(outputPath)
	}

	return result, nil
}
