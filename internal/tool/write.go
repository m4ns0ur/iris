package tool

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

// FileWriteTool writes content to a file inside the workspace.
type FileWriteTool struct {
	policy *policy.Policy
}

func NewFileWriteTool(pol *policy.Policy) *FileWriteTool {
	return &FileWriteTool{policy: pol}
}

func (t *FileWriteTool) Name() string {
	return "write"
}

func (t *FileWriteTool) Description() string {
	return "Write a file"
}

func (t *FileWriteTool) FullDescription() string {
	return "Write content to a file inside the workspace. Creates parent directories as needed and atomically overwrites existing files. Paths outside the workspace are rejected."
}

func (t *FileWriteTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "content"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path (absolute or relative to the workspace)",
			},
			"content": {
				Type:        "string",
				Description: "File content (may be empty)",
			},
		},
	}
}

func (t *FileWriteTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("missing required parameter: content")
	}

	if t.policy == nil {
		return nil, fmt.Errorf("%w: write tool has no security policy", ErrExecutionFailed)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolvedPath, err := t.policy.ResolveWrite(path)
	if err != nil {
		return nil, err
	}

	perm := fs.FileMode(0o644)
	if existingPerm, exists, err := existingFileMode(resolvedPath); err != nil {
		return nil, err
	} else if exists {
		perm = existingPerm
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create parent directories for %q: %w", resolvedPath, err)
	}

	// Re-resolve now that parents exist. This catches a path that became a
	// symlink pointing outside the workspace between the first check and the
	// write.
	resolvedPath, err = t.policy.ResolveWrite(path)
	if err != nil {
		return nil, err
	}

	if existingPerm, exists, err := existingFileMode(resolvedPath); err != nil {
		return nil, err
	} else if exists {
		perm = existingPerm
	}

	if err := writeFileAtomic(resolvedPath, []byte(content), perm); err != nil {
		return nil, fmt.Errorf("write failed %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
		"bytes":   len(content),
	}, nil
}
