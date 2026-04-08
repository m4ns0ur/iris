package tool

import (
	"context"
	"fmt"
	"path/filepath"
)

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

	// Resolve path relative to workdir if provided in context
	resolvedPath, err := t.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(filepath.Join(resolvedPath, pattern))
	if err != nil {
		return nil, fmt.Errorf("failed to search for files: %w", err)
	}

	return map[string]any{
		"matches": matches,
	}, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise uses current working directory.
func (t *SearchTool) resolvePath(ctx context.Context, path string) (string, error) {
	workdir, hasWorkdir := FromWorkdir(ctx)

	// If no workdir is set, use the current working directory
	if !hasWorkdir || workdir == "" {
		workdir = "."
	}

	// If path is absolute, use it directly
	if filepath.IsAbs(path) {
		return path, nil
	}

	// Resolve relative to workdir (or current directory if not set)
	resolved := filepath.Join(workdir, path)
	return resolved, nil
}
