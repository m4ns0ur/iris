package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FileReadTool reads the contents of a file.
type FileReadTool struct{}

// FileWriteTool writes content to a file.
type FileWriteTool struct{}

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

	// Resolve path relative to workdir if provided in context
	resolvedPath, err := t.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"content": string(content),
	}, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise returns as-is.
func (t *FileReadTool) resolvePath(ctx context.Context, path string) (string, error) {
	workdir, hasWorkdir := FromWorkdir(ctx)
	if !hasWorkdir || workdir == "" {
		// No workdir in context, return path as-is
		return path, nil
	}

	// If path is absolute, use it directly
	if filepath.IsAbs(path) {
		return path, nil
	}

	// Resolve relative to workdir
	resolved := filepath.Join(workdir, path)
	return resolved, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise returns as-is.
func (t *FileWriteTool) resolvePath(ctx context.Context, path string) (string, error) {
	workdir, hasWorkdir := FromWorkdir(ctx)
	if !hasWorkdir || workdir == "" {
		// No workdir in context, return path as-is
		return path, nil
	}

	// If path is absolute, use it directly
	if filepath.IsAbs(path) {
		return path, nil
	}

	// Resolve relative to workdir
	resolved := filepath.Join(workdir, path)
	return resolved, nil
}

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

	// Resolve path relative to workdir if provided in context
	resolvedPath, err := t.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(resolvedPath, []byte(content), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write file %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
	}, nil
}
