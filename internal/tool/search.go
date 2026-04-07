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

	matches, err := filepath.Glob(filepath.Join(path, pattern))
	if err != nil {
		return nil, fmt.Errorf("failed to search for files: %w", err)
	}

	return map[string]any{
		"matches": matches,
	}, nil
}
