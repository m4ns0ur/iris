package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileReadTool_WithWorkdir(t *testing.T) {
	// Create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "workdir-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test file in the temp directory
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "Hello from workdir!"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test with workdir context
	reg := NewToolRegistry()
	reg.Register("file_read", NewFileReadTool())

	ctx := WithWorkdir(context.Background(), tmpDir)

	result, err := reg.CallWithContext(ctx, "file_read", map[string]any{
		"path": "test.txt", // Relative path
	})
	if err != nil {
		t.Fatalf("Failed to read file with workdir: %v", err)
	}

	readContent, ok := result["content"].(string)
	if !ok {
		t.Fatal("Expected content in result")
	}
	if readContent != content {
		t.Errorf("Expected content '%s', got '%s'", content, readContent)
	}
}

func TestFileReadTool_WithoutWorkdir(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("file_read", NewFileReadTool())

	ctx := context.Background() // No workdir

	result, err := reg.CallWithContext(ctx, "file_read", map[string]any{
		"path": "/etc/hosts", // Absolute path should still work
	})
	if err != nil {
		t.Fatalf("Failed to read file without workdir: %v", err)
	}

	readContent, ok := result["content"].(string)
	if !ok {
		t.Fatal("Expected content in result")
	}
	if len(readContent) == 0 {
		t.Error("Expected non-empty content from /etc/hosts")
	}
}

func TestFileWriteTool_WithWorkdir(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "workdir-write-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	reg := NewToolRegistry()
	reg.Register("file_write", NewFileWriteTool())

	ctx := WithWorkdir(context.Background(), tmpDir)

	result, err := reg.CallWithContext(ctx, "file_write", map[string]any{
		"path":    "output.txt", // Relative path
		"content": "Test content written with workdir!",
	})
	if err != nil {
		t.Fatalf("Failed to write file with workdir: %v", err)
	}

	success, ok := result["success"].(bool)
	if !ok || !success {
		t.Fatal("Expected success in result")
	}

	// Verify the file was created at the correct location
	expectedPath := filepath.Join(tmpDir, "output.txt")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("File not created at expected path: %s", expectedPath)
	} else {
		// Read and verify content
		actualContent, _ := os.ReadFile(expectedPath)
		if string(actualContent) != "Test content written with workdir!" {
			t.Errorf("Expected 'Test content written with workdir!', got '%s'", string(actualContent))
		}
	}
}

func TestSearchTool_WithWorkdir(t *testing.T) {
	// Create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "workdir-search-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	if err := os.WriteFile(file1, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	if err := os.WriteFile(file2, []byte("data"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	reg := NewToolRegistry()
	reg.Register("search", NewSearchTool())

	ctx := WithWorkdir(context.Background(), tmpDir)

	result, err := reg.CallWithContext(ctx, "search", map[string]any{
		"path":    ".", // Relative path from workdir
		"pattern": "*.txt",
	})
	if err != nil {
		t.Fatalf("Failed to search with workdir: %v", err)
	}

	matches, ok := result["matches"].([]string)
	if !ok {
		t.Fatal("Expected matches in result")
	}

	if len(matches) < 2 {
		t.Errorf("Expected at least 2 matches, got %d", len(matches))
	}

	// Verify all matches are within workdir
	for _, match := range matches {
		if !filepath.IsAbs(match) && filepath.Dir(match) != tmpDir {
			t.Errorf("Match '%s' is not within workdir '%s'", match, tmpDir)
		}
	}
}

func TestShellTool_WithWorkdir(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "workdir-shell-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	reg := NewToolRegistry()
	reg.Register("shell", NewShellTool())

	ctx := WithWorkdir(context.Background(), tmpDir)

	result, err := reg.CallWithContext(ctx, "shell", map[string]any{
		"command": "pwd", // Should return workdir path
	})
	if err != nil {
		t.Fatalf("Failed to execute shell command with workdir: %v", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		t.Fatal("Expected output in result")
	}

	// pwd should return the workdir path (may have trailing newline differences)
	cleanOutput := strings.TrimSpace(filepath.Clean(output))
	if cleanOutput != tmpDir {
		t.Errorf("Expected output to be '%s', got '%s'", tmpDir, cleanOutput)
	}
}

func TestShellTool_WithoutWorkdir(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("shell", NewShellTool())

	ctx := context.Background() // No workdir

	result, err := reg.CallWithContext(ctx, "shell", map[string]any{
		"command": "pwd",
	})
	if err != nil {
		t.Fatalf("Failed to execute shell command without workdir: %v", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		t.Fatal("Expected output in result")
	}

	// Should return current working directory (not empty)
	if len(output) == 0 {
		t.Error("Expected non-empty output from pwd")
	}
}

func TestGrepTool_WithWorkdir(t *testing.T) {
	// Create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "workdir-grep-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test files with content
	file1 := filepath.Join(tmpDir, "file1.txt")
	content := "test pattern here"
	if err := os.WriteFile(file1, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	reg := NewToolRegistry()
	reg.Register("grep", NewGrepTool())

	ctx := WithWorkdir(context.Background(), tmpDir)

	result, err := reg.CallWithContext(ctx, "grep", map[string]any{
		"path":    ".", // Relative path from workdir
		"pattern": content,
	})
	if err != nil {
		t.Fatalf("Failed to grep with workdir: %v", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		t.Fatal("Expected output in result")
	}

	if len(output) == 0 {
		t.Error("Expected non-empty output from grep")
	}

	// Verify output contains the pattern and file path within workdir
	if !containsString(output, content) || !containsString(output, tmpDir) {
		t.Errorf("Output should contain both pattern and workdir path. Got: %s", output)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestFileReadTool_FallbackToCurrentDir(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("file_read", NewFileReadTool())

	ctx := context.Background() // No workdir set

	// Read a file in current directory (should use cwd as fallback)
	result, err := reg.CallWithContext(ctx, "file_read", map[string]any{
		"path": "/etc/hosts", // Absolute path should still work
	})
	if err != nil {
		t.Fatalf("Failed to read file without workdir: %v", err)
	}

	readContent, ok := result["content"].(string)
	if !ok {
		t.Fatal("Expected content in result")
	}
	if len(readContent) == 0 {
		t.Error("Expected non-empty content from /etc/hosts")
	}
}

func TestFileWriteTool_FallbackToCurrentDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fallback-write-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	reg := NewToolRegistry()
	reg.Register("file_write", NewFileWriteTool())

	ctx := context.Background() // No workdir set

	result, err := reg.CallWithContext(ctx, "file_write", map[string]any{
		"path":    filepath.Join(tmpDir, "fallback.txt"), // Absolute path should work
		"content": "Test fallback to current directory!",
	})
	if err != nil {
		t.Fatalf("Failed to write file without workdir: %v", err)
	}

	success, ok := result["success"].(bool)
	if !ok || !success {
		t.Fatal("Expected success in result")
	}

	// Verify the file was created at the absolute path
	expectedPath := filepath.Join(tmpDir, "fallback.txt")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("File not created at expected path: %s", expectedPath)
	} else {
		actualContent, _ := os.ReadFile(expectedPath)
		if string(actualContent) != "Test fallback to current directory!" {
			t.Errorf("Expected 'Test fallback to current directory!', got '%s'", string(actualContent))
		}
	}
}

func TestSearchTool_FallbackToCurrentDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fallback-search-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test file in temp directory
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	reg := NewToolRegistry()
	reg.Register("search", NewSearchTool())

	ctx := context.Background() // No workdir set

	result, err := reg.CallWithContext(ctx, "search", map[string]any{
		"path":    tmpDir, // Absolute path should work
		"pattern": "*.txt",
	})
	if err != nil {
		t.Fatalf("Failed to search without workdir: %v", err)
	}

	matches, ok := result["matches"].([]string)
	if !ok {
		t.Fatal("Expected matches in result")
	}

	if len(matches) < 1 {
		t.Errorf("Expected at least 1 match, got %d", len(matches))
	}

	// Verify the match is within the temp directory
	for _, match := range matches {
		if !filepath.IsAbs(match) || filepath.Dir(match) != tmpDir {
			t.Errorf("Match '%s' should be within temp dir '%s'", match, tmpDir)
		}
	}
}

func TestGrepTool_FallbackToCurrentDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fallback-grep-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test file in temp directory with content
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "grep fallback pattern"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	reg := NewToolRegistry()
	reg.Register("grep", NewGrepTool())

	ctx := context.Background() // No workdir set

	result, err := reg.CallWithContext(ctx, "grep", map[string]any{
		"path":    tmpDir, // Absolute path should work
		"pattern": content,
	})
	if err != nil {
		t.Fatalf("Failed to grep without workdir: %v", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		t.Fatal("Expected output in result")
	}

	if len(output) == 0 {
		t.Error("Expected non-empty output from grep")
	}

	// Verify output contains the pattern and file path within temp directory
	if !containsString(output, content) || !containsString(output, tmpDir) {
		t.Errorf("Output should contain both pattern and temp dir path. Got: %s", output)
	}
}
