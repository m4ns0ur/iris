package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

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

	// Resolve path relative to workdir if provided in context
	resolvedPath, err := t.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	output, err := bash(ctx, fmt.Sprintf("grep -r '%s' %s 2>/dev/null", escapeGrepPattern(pattern), resolvedPath))
	if err != nil {
		return nil, fmt.Errorf("failed to grep: %w", err)
	}

	return map[string]any{
		"output": output,
	}, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise returns as-is.
func (t *GrepTool) resolvePath(ctx context.Context, path string) (string, error) {
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

// Escape special characters in grep pattern
func escapeGrepPattern(pattern string) string {
	// Simple escaping for common special chars
	pattern = strings.ReplaceAll(pattern, `\`, `\\`)
	pattern = strings.ReplaceAll(pattern, `"`, `\"`)
	return pattern
}
