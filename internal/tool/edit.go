package tool

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
)

// FileEditTool makes precise edits to a file based on exact text replacement.
type FileEditTool struct{}

func NewFileEditTool() *FileEditTool {
	return &FileEditTool{}
}

// FileWriteTool writes content to a file.
func (t *FileEditTool) Name() string {
	return "edit"
}

func (t *FileEditTool) Description() string {
	return "Edit a file"
}

func (t *FileEditTool) FullDescription() string {
	return "Edit a file via exact text replacements. Each find must uniquely match and not overlap with other edits. Merge related changes into a single edit."
}

func (t *FileEditTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "edits"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path",
			},
			"edits": {
				Type:        "array",
				Description: "Replacements",
				Items: &llm.Parameters{
					Type:     "object",
					Required: []string{"find", "replace"},
					Properties: map[string]llm.Property{
						"find": {
							Type:        "string",
							Description: "Exact text",
						},
						"replace": {
							Type:        "string",
							Description: "Replacement text",
						},
					},
				},
			},
		},
	}
}

func (t *FileEditTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	edits, ok := args["edits"].([]any)
	if !ok || len(edits) == 0 {
		return nil, fmt.Errorf("missing required parameter: edits")
	}

	resolvedPath, err := resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read failed %q: %w", resolvedPath, err)
	}

	content := string(data)

	for _, edit := range edits {
		editMap, ok := edit.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid edit format")
		}
		find, ok := editMap["find"].(string)
		if !ok || find == "" {
			return nil, fmt.Errorf("missing required field in edit: find")
		}
		replace, ok := editMap["replace"].(string)
		if !ok {
			return nil, fmt.Errorf("missing required field in edit: replace")
		}

		if !strings.Contains(content, find) {
			return nil, fmt.Errorf("text to find not found in file: %q", find)
		}

		content = strings.Replace(content, find, replace, 1)
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write failed %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
	}, nil
}
