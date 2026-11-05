package tool

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/internal/policy"
)

func newShellTestRegistry(t *testing.T, shell policy.ShellOptions, approver policy.Approver) (*ToolRegistry, *policy.Policy) {
	t.Helper()

	options := policy.Options{
		Shell:    shell,
		Approver: approver,
	}
	if shell.Mode == policy.ShellModeAllow {
		options.ExternalSandbox = true
	}

	pol, err := policy.New(t.TempDir(), options)
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	registry, err := NewDefaultRegistry(pol)
	if err != nil {
		t.Fatalf("NewDefaultRegistry() error = %v", err)
	}
	t.Cleanup(func() {
		_ = registry.Close()
	})

	return registry, pol
}

func executeShell(t *testing.T, registry *ToolRegistry, ctx context.Context, command string) (map[string]any, error) {
	t.Helper()

	shell := registry.Get("shell")
	if shell == nil {
		t.Fatal("shell tool not registered")
	}
	return shell.Execute(ctx, map[string]any{"command": command})
}

func TestShellToolTimeoutKillsProcess(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 300 * time.Millisecond,
	}, nil)

	start := time.Now()
	result, err := executeShell(t, registry, context.Background(), "sleep 5")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if timedOut, _ := result["timed_out"].(bool); !timedOut {
		t.Fatalf("timed_out = %v, want true (result %+v)", result["timed_out"], result)
	}
	if success, _ := result["success"].(bool); success {
		t.Fatal("success = true for timed out command")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestShellToolTimeoutKillsChildProcesses(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 300 * time.Millisecond,
	}, nil)

	start := time.Now()
	result, err := executeShell(t, registry, context.Background(), "sleep 5 & wait")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if timedOut, _ := result["timed_out"].(bool); !timedOut {
		t.Fatalf("timed_out = false, want true (result %+v)", result)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("child processes survived timeout; elapsed %s", elapsed)
	}
}

func TestShellToolCancellation(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 10 * time.Second,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := executeShell(t, registry, ctx, "sleep 5")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if canceled, _ := result["canceled"].(bool); !canceled {
		t.Fatalf("canceled = false, want true (result %+v)", result)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}
}

func TestShellToolReturnsOutputForNonZeroExit(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 5 * time.Second,
	}, nil)

	result, err := executeShell(t, registry, context.Background(), "echo fail; exit 3")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if code, ok := result["exit_code"].(int); !ok || code != 3 {
		t.Fatalf("exit_code = %v, want 3", result["exit_code"])
	}
	if success, _ := result["success"].(bool); success {
		t.Fatal("success = true for failing command")
	}
	if output, _ := result["output"].(string); !strings.Contains(output, "fail") {
		t.Fatalf("output = %q, want it to contain fail", output)
	}
}

func TestShellToolBoundsOutputAndKeepsFullFile(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         10 * time.Second,
		MaxOutputBytes:  256,
		MaxOutputLines:  5,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	result, err := executeShell(
		t,
		registry,
		context.Background(),
		"for i in $(seq 1 200); do echo line-$i; done",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	output, _ := result["output"].(string)
	if !strings.Contains(output, "line-200") {
		t.Fatalf("output = %q, want tail containing line-200", output)
	}
	if len(output) > 256 {
		t.Fatalf("output length = %d, want <= 256", len(output))
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatal("truncated = false, want true")
	}

	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("full_output_file missing (result %+v)", result)
	}

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if !strings.Contains(string(full), "line-1") {
		t.Fatalf("full output does not contain the beginning: %q", string(full[:min(len(full), 100)]))
	}
	if len(full) <= 256 {
		t.Fatalf("full output length = %d, want > 256", len(full))
	}

	tempDir := registry.TempDir()
	if err := registry.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp dir %q still exists after Close: %v", tempDir, err)
	}
}

func TestShellToolRemovesTempFileForSmallOutput(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 5 * time.Second,
	}, nil)

	result, err := executeShell(t, registry, context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if _, ok := result["full_output_file"]; ok {
		t.Fatalf("full_output_file present for untruncated output: %+v", result)
	}

	entries, err := os.ReadDir(registry.TempDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestShellToolDeniesCommand(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeAllow,
		Timeout: 5 * time.Second,
	}, nil)

	_, err := executeShell(t, registry, context.Background(), "sudo echo hi")
	if !errors.Is(err, policy.ErrCommandDenied) {
		t.Fatalf("Execute() error = %v, want ErrCommandDenied", err)
	}
}

func TestShellToolFailsClosedWithoutApprover(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{}, nil)

	_, err := executeShell(t, registry, context.Background(), "echo hi")
	if !errors.Is(err, policy.ErrApprovalUnavailable) {
		t.Fatalf("Execute() error = %v, want ErrApprovalUnavailable", err)
	}
}

func TestShellToolApproverAllows(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeApproval,
		Timeout: 5 * time.Second,
	}, policy.ApproverFunc(func(context.Context, policy.ApprovalRequest) (policy.ApprovalDecision, error) {
		return policy.ApprovalDecision{Allow: true}, nil
	}))

	result, err := executeShell(t, registry, context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if output, _ := result["output"].(string); !strings.Contains(output, "hi") {
		t.Fatalf("output = %q, want hi", output)
	}
}

func TestShellToolApproverDenies(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:    policy.ShellModeApproval,
		Timeout: 5 * time.Second,
	}, policy.ApproverFunc(func(context.Context, policy.ApprovalRequest) (policy.ApprovalDecision, error) {
		return policy.ApprovalDecision{Allow: false, Reason: "denied in test"}, nil
	}))

	_, err := executeShell(t, registry, context.Background(), "echo hi")
	if !errors.Is(err, policy.ErrCommandDenied) {
		t.Fatalf("Execute() error = %v, want ErrCommandDenied", err)
	}
}

func TestShellToolReadOnlyMode(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{
		Mode:            policy.ModeReadOnly,
		ExternalSandbox: true,
		Shell:           policy.ShellOptions{Mode: policy.ShellModeAllow},
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	registry, err := NewDefaultRegistry(pol)
	if err != nil {
		t.Fatalf("NewDefaultRegistry() error = %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	_, err = executeShell(t, registry, context.Background(), "echo hi")
	if !errors.Is(err, policy.ErrShellDisabled) {
		t.Fatalf("Execute() error = %v, want ErrShellDisabled", err)
	}
}

func TestShellToolRespectsShellDenyMode(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode: policy.ShellModeDeny,
	}, nil)

	_, err := executeShell(t, registry, context.Background(), "echo hi")
	if !errors.Is(err, policy.ErrShellDisabled) {
		t.Fatalf("Execute() error = %v, want ErrShellDisabled", err)
	}
}

func TestReadToolCanReadCapturedShellOutput(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         10 * time.Second,
		MaxOutputBytes:  64,
		MaxOutputLines:  2,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	result, err := executeShell(
		t,
		registry,
		context.Background(),
		"for i in $(seq 1 100); do echo line-$i; done",
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("full_output_file missing (result %+v)", result)
	}

	readResult, err := registry.Get("read").Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read Execute() error = %v", err)
	}

	content, _ := readResult["content"].(string)
	if !strings.Contains(content, "line-1\n") {
		t.Fatalf("captured output read = %q, want it to contain line-1", content)
	}
	if !strings.Contains(content, "line-100") {
		t.Fatalf("captured output read = %q, want it to contain line-100", content)
	}
}
