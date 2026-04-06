package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileReadTool reads the contents of a file.
type FileReadTool struct{}

func NewFileReadTool() *FileReadTool {
	return &FileReadTool{}
}

func (t *FileReadTool) Name() string {
	return "file_read"
}

func (t *FileReadTool) Description() string {
	return "Read the contents of a file. Returns an error if the file does not exist or cannot be read."
}

func (t *FileReadTool) Parameters() map[string]any {
	return map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to the file to read",
			"required":    true,
		},
	}
}

func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %q: %w", path, err)
	}

	return map[string]any{
		"content": string(content),
	}, nil
}

// FileWriteTool writes content to a file.
type FileWriteTool struct{}

func NewFileWriteTool() *FileWriteTool {
	return &FileWriteTool{}
}

func (t *FileWriteTool) Name() string {
	return "file_write"
}

func (t *FileWriteTool) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, overwrites if it does."
}

func (t *FileWriteTool) Parameters() map[string]any {
	return map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to the file to write",
			"required":    true,
		},
		"content": map[string]any{
			"type":        "string",
			"description": "Content to write to the file",
			"required":    true,
		},
	}
}

func (t *FileWriteTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("missing required parameter: content")
	}

	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write file %q: %w", path, err)
	}

	return map[string]any{
		"success": true,
	}, nil
}

// FileListTool lists files and directories in a directory.
type FileListTool struct{}

func NewFileListTool() *FileListTool {
	return &FileListTool{}
}

func (t *FileListTool) Name() string {
	return "file_list"
}

func (t *FileListTool) Description() string {
	return "List files and directories in a directory. Returns an error if the path is invalid or not a directory."
}

func (t *FileListTool) Parameters() map[string]any {
	return map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to the directory to list",
			"required":    true,
		},
	}
}

func (t *FileListTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory %q: %w", path, err)
	}

	var result []string
	for _, entry := range entries {
		result = append(result, entry.Name())
	}

	return map[string]any{
		"entries": result,
	}, nil
}

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

	output, err := bash(command)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	return map[string]any{
		"output": output,
	}, nil
}

// SearchTool searches for files matching a pattern.
type SearchTool struct{}

func NewSearchTool() *SearchTool {
	return &SearchTool{}
}

func (t *SearchTool) Name() string {
	return "search"
}

func (t *SearchTool) Description() string {
	return "Find files matching a glob pattern in the current directory or subdirectories."
}

func (t *SearchTool) Parameters() map[string]any {
	return map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Starting path to search from (defaults to current directory)",
			"required":    false,
			"default":     ".",
		},
		"pattern": map[string]any{
			"type":        "string",
			"description": "Glob pattern to match files against",
			"required":    true,
		},
	}
}

func (t *SearchTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path := "."
	if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}

	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("missing required parameter: pattern")
	}

	matches, err := filepath.Glob(filepath.Join(path, pattern))
	if err != nil {
		return nil, fmt.Errorf("failed to search for files: %w", err)
	}

	return map[string]any{
		"matches": matches,
	}, nil
}

// GrepTool searches for text patterns in files.
type GrepTool struct{}

func NewGrepTool() *GrepTool {
	return &GrepTool{}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return "Search for a pattern in files. Returns matching lines with file paths."
}

func (t *GrepTool) Parameters() map[string]any {
	return map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to search in (file or directory)",
			"required":    true,
		},
		"pattern": map[string]any{
			"type":        "string",
			"description": "Pattern to search for",
			"required":    true,
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("missing required parameter: pattern")
	}

	output, err := bash(fmt.Sprintf("grep -r '%s' %s 2>/dev/null", escapeGrepPattern(pattern), path))
	if err != nil {
		return nil, fmt.Errorf("failed to grep: %w", err)
	}

	return map[string]any{
		"output": output,
	}, nil
}

// Escape special characters in grep pattern
func escapeGrepPattern(pattern string) string {
	// Simple escaping for common special chars
	pattern = strings.ReplaceAll(pattern, `\`, `\\`)
	pattern = strings.ReplaceAll(pattern, `"`, `\"`)
	return pattern
}
