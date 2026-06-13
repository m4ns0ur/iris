package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/m4ns0ur/iris/internal/llm"
)

// FileWriteTool writes content to a file.
type FileWriteTool struct{}

func NewFileWriteTool() *FileWriteTool {
	return &FileWriteTool{}
}

// FileWriteTool writes content to a file.
func (t *FileWriteTool) Name() string {
	return "write"
}

func (t *FileWriteTool) Description() string {
	return "Write a file"
}

func (t *FileWriteTool) FullDescription() string {
	return "Write content to a file. Creates parent directories as needed. Overwrites existing files."
}

func (t *FileWriteTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "content"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path",
			},
			"content": {
				Type:        "string",
				Description: "File content",
			},
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

	resolvedPath, err := resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create dirs: %w", err)
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write failed %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
	}, nil
}
