package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/m4ns0ur/iris/internal/policy"
)

func newFilePolicy(t *testing.T, mode policy.Mode) *policy.Policy {
	t.Helper()

	root := t.TempDir()
	pol, err := policy.New(root, policy.Options{Mode: mode})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}
	return pol
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

func editArgs(path string, edits ...map[string]any) map[string]any {
	list := make([]any, 0, len(edits))
	for _, edit := range edits {
		list = append(list, edit)
	}
	return map[string]any{"path": path, "edits": list}
}

func edit(find, replace string) map[string]any {
	return map[string]any{"find": find, "replace": replace}
}

func TestReadToolReadsContent(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	writeTestFile(t, filepath.Join(pol.Root(), "file.txt"), "alpha\nbeta\n")

	result, err := NewFileReadTool(pol).Execute(context.Background(), map[string]any{"path": "file.txt"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := result["content"].(string); got != "alpha\nbeta\n" {
		t.Fatalf("content = %q, want %q", got, "alpha\nbeta\n")
	}
}

func TestReadToolStartAndMaxLines(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	writeTestFile(t, filepath.Join(pol.Root(), "file.txt"), "one\ntwo\nthree\nfour\nfive\n")

	result, err := NewFileReadTool(pol).Execute(context.Background(), map[string]any{
		"path":  "file.txt",
		"start": float64(3),
		"max":   float64(2),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := result["content"].(string); got != "three\nfour\n" {
		t.Fatalf("content = %q, want %q", got, "three\nfour\n")
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatal("truncated = false, want true")
	}
}

func TestReadToolRejectsOutsidePath(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeTestFile(t, outside, "secret")

	_, err := NewFileReadTool(pol).Execute(context.Background(), map[string]any{"path": outside})
	if !errors.Is(err, policy.ErrPathOutsideWorkspace) {
		t.Fatalf("Execute() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestReadToolRejectsDirectory(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)

	_, err := NewFileReadTool(pol).Execute(context.Background(), map[string]any{"path": "."})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("Execute() error = %v, want directory error", err)
	}
}

func TestReadToolRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "secret")

	if err := os.Symlink(outside, filepath.Join(pol.Root(), "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := NewFileReadTool(pol).Execute(context.Background(), map[string]any{"path": "link/secret.txt"})
	if !errors.Is(err, policy.ErrPathOutsideWorkspace) {
		t.Fatalf("Execute() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestWriteToolCreatesNestedFile(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)

	result, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "a/b/c.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if success, _ := result["success"].(bool); !success {
		t.Fatal("success = false, want true")
	}

	got := readTestFile(t, filepath.Join(pol.Root(), "a", "b", "c.txt"))
	if got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
}

func TestWriteToolAllowsEmptyContent(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)

	if _, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "empty.txt",
		"content": "",
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := readTestFile(t, filepath.Join(pol.Root(), "empty.txt")); got != "" {
		t.Fatalf("content = %q, want empty", got)
	}
}

func TestWriteToolRejectsOutsidePath(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	outside := filepath.Join(t.TempDir(), "file.txt")

	_, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    outside,
		"content": "nope",
	})
	if !errors.Is(err, policy.ErrPathOutsideWorkspace) {
		t.Fatalf("Execute() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestWriteToolRejectsSymlinkedParentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}

	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(pol.Root(), "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "link/new.txt",
		"content": "nope",
	})
	if !errors.Is(err, policy.ErrPathOutsideWorkspace) {
		t.Fatalf("Execute() error = %v, want ErrPathOutsideWorkspace", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "new.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside file was created: %v", statErr)
	}
}

func TestWriteToolPreservesMode(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "old")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if _, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "file.txt",
		"content": "new",
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteToolRejectsDirectory(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	if err := os.MkdirAll(filepath.Join(pol.Root(), "dir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "dir",
		"content": "data",
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Execute() error = %v, want not-a-regular-file error", err)
	}
}

func TestWriteToolLeavesNoTempFiles(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)

	if _, err := NewFileWriteTool(pol).Execute(context.Background(), map[string]any{
		"path":    "file.txt",
		"content": "data",
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(pol.Root(), ".iris-write-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestEditToolUniqueReplacement(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "hello world\n")

	result, err := NewFileEditTool(pol).Execute(context.Background(), editArgs("file.txt", edit("world", "iris")))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if changed, _ := result["changed"].(bool); !changed {
		t.Fatal("changed = false, want true")
	}

	if got := readTestFile(t, path); got != "hello iris\n" {
		t.Fatalf("content = %q, want %q", got, "hello iris\n")
	}
}

func TestEditToolRejectsAmbiguousFind(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "foo foo\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs("file.txt", edit("foo", "bar")))
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("Execute() error = %v, want uniqueness error", err)
	}

	if got := readTestFile(t, path); got != "foo foo\n" {
		t.Fatalf("file changed despite rejected edit: %q", got)
	}
}

func TestEditToolRejectsOverlappingEdits(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "abcdef\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(
		"file.txt",
		edit("abc", "x"),
		edit("bcd", "y"),
	))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("Execute() error = %v, want overlap error", err)
	}

	if got := readTestFile(t, path); got != "abcdef\n" {
		t.Fatalf("file changed despite rejected edits: %q", got)
	}
}

func TestEditToolSameFindTwiceIsRejected(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "abc\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(
		"file.txt",
		edit("abc", "x"),
		edit("abc", "y"),
	))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("Execute() error = %v, want overlap error", err)
	}
}

func TestEditToolAppliesMultipleEdits(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "alpha\nbeta\ngamma\n")

	if _, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(
		"file.txt",
		edit("alpha", "A"),
		edit("gamma", "G"),
	)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := readTestFile(t, path); got != "A\nbeta\nG\n" {
		t.Fatalf("content = %q, want %q", got, "A\nbeta\nG\n")
	}
}

func TestEditToolReplacementMayContainFind(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "aaa\n")

	if _, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(
		"file.txt",
		edit("aaa", "aaa bbb"),
	)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := readTestFile(t, path); got != "aaa bbb\n" {
		t.Fatalf("content = %q, want %q", got, "aaa bbb\n")
	}
}

func TestEditToolEmptyReplacement(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "remove me\n")

	if _, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(
		"file.txt",
		edit("remove me", ""),
	)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := readTestFile(t, path); got != "\n" {
		t.Fatalf("content = %q, want newline", got)
	}
}

func TestEditToolMissingFind(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	writeTestFile(t, filepath.Join(pol.Root(), "file.txt"), "abc\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs("file.txt", edit("zzz", "x")))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Execute() error = %v, want not-found error", err)
	}
}

func TestEditToolRejectsOutsidePath(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	outside := filepath.Join(t.TempDir(), "file.txt")
	writeTestFile(t, outside, "abc\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs(outside, edit("abc", "x")))
	if !errors.Is(err, policy.ErrPathOutsideWorkspace) {
		t.Fatalf("Execute() error = %v, want ErrPathOutsideWorkspace", err)
	}
}

func TestEditToolReadOnlyModeDenies(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeReadOnly)
	writeTestFile(t, filepath.Join(pol.Root(), "file.txt"), "abc\n")

	_, err := NewFileEditTool(pol).Execute(context.Background(), editArgs("file.txt", edit("abc", "x")))
	if !errors.Is(err, policy.ErrReadOnlyMode) {
		t.Fatalf("Execute() error = %v, want ErrReadOnlyMode", err)
	}
}

func TestEditToolPreservesMode(t *testing.T) {
	pol := newFilePolicy(t, policy.ModeWorkspaceWrite)
	path := filepath.Join(pol.Root(), "file.txt")
	writeTestFile(t, path, "abc\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if _, err := NewFileEditTool(pol).Execute(context.Background(), editArgs("file.txt", edit("abc", "x"))); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}
