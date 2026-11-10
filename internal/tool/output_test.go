package tool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

func TestCommonOutputPolicyTruncatesLargeResult(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         time.Second,
		MaxOutputBytes:  512,
		MaxOutputLines:  10,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	var builder strings.Builder
	for index := 1; index <= 500; index++ {
		fmt.Fprintf(&builder, "line-%03d\n", index)
	}
	full := builder.String()

	result := registry.applyOutput("custom", map[string]any{"output": full})
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatalf("result was not truncated: %+v", result)
	}
	output, _ := result["output"].(string)
	if !strings.Contains(output, "line-001\n") || !strings.Contains(output, "line-500") {
		t.Fatalf("preview does not preserve head and tail: %q", output)
	}
	notice, _ := result["truncation_notice"].(string)
	if !strings.Contains(notice, "Output truncated.") ||
		!strings.Contains(notice, "Full output:") ||
		!strings.Contains(notice, "lines omitted") {
		t.Fatalf("truncation notice = %q", notice)
	}
	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("full_output_file missing: %+v", result)
	}
	fullStored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(fullStored) != full {
		t.Fatalf("stored full output differs from original")
	}
	if total, _ := result["output_total_lines"].(int); total != 500 {
		t.Fatalf("total lines = %v, want 500", result["output_total_lines"])
	}
	if omitted, _ := result["output_omitted_lines"].(int); omitted <= 0 {
		t.Fatalf("omitted lines = %v, want > 0", result["output_omitted_lines"])
	}

	readResult, err := registry.Get("read").Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read full output error = %v", err)
	}
	content, _ := readResult["content"].(string)
	if !strings.Contains(content, "line-001\n") {
		t.Fatalf("read full output = %q", content)
	}
}

func TestCommonOutputPolicyLeavesSmallResult(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         time.Second,
		MaxOutputBytes:  1024,
		MaxOutputLines:  100,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	result := registry.applyOutput("custom", map[string]any{"output": "ok"})
	if _, ok := result["truncated"]; ok {
		t.Fatalf("small result was marked truncated: %+v", result)
	}
	if _, ok := result["truncation_notice"]; ok {
		t.Fatalf("small result has a truncation notice: %+v", result)
	}
}

func TestCommonOutputPolicyAppliesThroughExecuteWithTools(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{
		Shell: policy.ShellOptions{
			Mode:            policy.ShellModeAllow,
			Timeout:         time.Second,
			MaxOutputBytes:  256,
			MaxOutputLines:  5,
			MaxCaptureBytes: 1 << 20,
		},
		ExternalSandbox: true,
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}
	large := toolStub{
		name: "large",
		result: map[string]any{
			"output": strings.Repeat("large output line\n", 200),
		},
	}
	registry, err := NewDefaultRegistry(pol, large)
	if err != nil {
		t.Fatalf("NewDefaultRegistry() error = %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	result, err := ExecuteWithTools(context.Background(), registry, "large", map[string]any{})
	if err != nil {
		t.Fatalf("ExecuteWithTools() error = %v", err)
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatalf("custom tool output was not truncated: %+v", result)
	}
	if _, ok := result["full_output_file"].(string); !ok {
		t.Fatalf("custom tool full output file missing: %+v", result)
	}
}

func TestPersistentOutputDirKeepsFiles(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         time.Second,
		MaxOutputBytes:  128,
		MaxOutputLines:  3,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	oldDir := registry.TempDir()
	persistentDir := t.TempDir()
	if err := registry.SetOutputDir(persistentDir); err != nil {
		t.Fatalf("SetOutputDir() error = %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("ephemeral output directory still exists: %v", err)
	}

	result := registry.applyOutput("custom", map[string]any{
		"output": strings.Repeat("persistent output\n", 100),
	})
	path, _ := result["full_output_file"].(string)
	if path == "" || !strings.HasPrefix(path, persistentDir) {
		t.Fatalf("full output path = %q, want under %q", path, persistentDir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("full output mode = %v, want 0600", info.Mode().Perm())
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persistent output was removed on Close: %v", err)
	}
	readTool := registry.Get("read")
	if readTool == nil {
		t.Fatal("read tool unavailable")
	}
	if _, err := readTool.Execute(context.Background(), map[string]any{"path": path}); err != nil {
		t.Fatalf("read persistent output error = %v", err)
	}
}

type toolStub struct {
	name   string
	result map[string]any
}

func (t toolStub) Name() string               { return t.name }
func (t toolStub) Description() string        { return t.name }
func (t toolStub) FullDescription() string    { return t.name }
func (t toolStub) Parameters() llm.Parameters { return llm.Parameters{Type: "object"} }
func (t toolStub) Execute(context.Context, map[string]any) (map[string]any, error) {
	return t.result, nil
}

func TestShellOutputUsesCommonNotice(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         10 * time.Second,
		MaxOutputBytes:  256,
		MaxOutputLines:  5,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	result, err := ExecuteWithTools(
		context.Background(),
		registry,
		"shell",
		map[string]any{"command": "for i in $(seq 1 200); do echo line-$i; done"},
	)
	if err != nil {
		t.Fatalf("shell error = %v", err)
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatalf("shell output was not truncated: %+v", result)
	}
	notice, _ := result["truncation_notice"].(string)
	if !strings.Contains(notice, "Output truncated.") || !strings.Contains(notice, "Full output:") {
		t.Fatalf("shell truncation notice = %q", notice)
	}
	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("shell full output file missing: %+v", result)
	}
	totalLines := intValue(result, "total_output_lines")
	startLine := intValue(result, "output_start_line")
	if totalLines != 200 || startLine <= 1 {
		t.Fatalf("shell line metadata = total %d start %d, want total 200 and tail start > 1", totalLines, startLine)
	}
	readResult, err := registry.Get("read").Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read shell full output error = %v", err)
	}
	content, _ := readResult["content"].(string)
	if !strings.Contains(content, "line-1\n") || !strings.Contains(content, "line-200") {
		t.Fatalf("read shell full output = %q", content)
	}
}

func TestCommonOutputPolicyCapsLargeStructuredResult(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         time.Second,
		MaxOutputBytes:  512,
		MaxOutputLines:  20,
		MaxCaptureBytes: 1 << 20,
	}, nil)

	items := make([]any, 0, 200)
	for index := 0; index < 200; index++ {
		items = append(items, fmt.Sprintf("item-%03d", index))
	}
	result := registry.applyOutput("structured", map[string]any{
		"success": true,
		"items":   items,
	})

	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatalf("structured result was not capped: %+v", result)
	}
	notice, _ := result["truncation_notice"].(string)
	if !strings.Contains(notice, "Tool result was too large") || !strings.Contains(notice, "Full output:") {
		t.Fatalf("structured notice = %q", notice)
	}
	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("structured full output missing: %+v", result)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if !strings.Contains(string(stored), "item-199") {
		t.Fatalf("stored structured result is not full")
	}
}

func TestCommonOutputPolicyCapsStoredFullOutput(t *testing.T) {
	registry, _ := newShellTestRegistry(t, policy.ShellOptions{
		Mode:            policy.ShellModeAllow,
		Timeout:         time.Second,
		MaxOutputBytes:  64,
		MaxOutputLines:  4,
		MaxCaptureBytes: 128,
	}, nil)

	result := registry.applyOutput("custom", map[string]any{
		"output": strings.Repeat("stored output line\n", 100),
	})
	if fullTruncated, _ := result["full_output_truncated"].(bool); !fullTruncated {
		t.Fatalf("full output was not marked as capped: %+v", result)
	}
	path, _ := result["full_output_file"].(string)
	if path == "" {
		t.Fatalf("full output path missing: %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if info.Size() != 128 {
		t.Fatalf("stored full output size = %d, want 128", info.Size())
	}
	notice, _ := result["truncation_notice"].(string)
	if !strings.Contains(notice, "capped") {
		t.Fatalf("truncation notice does not mention the capture cap: %q", notice)
	}
}
