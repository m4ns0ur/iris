package tool

import (
	"context"
	"fmt"
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
