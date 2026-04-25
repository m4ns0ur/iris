package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
)

// FileReadTool reads the contents of a file.
type FileReadTool struct{}

func NewFileReadTool() *FileReadTool {
	return &FileReadTool{}
}

func (t *FileReadTool) Name() string {
	return "read"
}

func (t *FileReadTool) Description() string {
	return "Read a file"
}

func (t *FileReadTool) FullDescription() string {
	return "Read the contents of a file. Output is truncated to 1000 lines. You can read a large file in parts by specifying 'start' and 'max' parameters."
}

func (t *FileReadTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "Path to the file to read",
			},
			"start": {
				Type:        "number",
				Description: "Start line number for partial reads (0-indexed)",
			},
			"max": {
				Type:        "number",
				Description: "Maximum number of bytes to read (for large files)",
			},
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

	start := 0
	if s, ok := args["start"].(float64); ok && s >= 0 {
		start = int(s)
	}

	max := 1000 * 1024 // 1000 KB
	if m, ok := args["max"].(float64); ok && m > 0 {
		max = int(m)
	}

	fi, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file %q: %w", resolvedPath, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %q", resolvedPath)
	}

	f, err := os.Open(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %q: %w", resolvedPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var contentBuilder strings.Builder
	lineNum := 0
	bytesRead := 0
	for scanner.Scan() {
		if lineNum >= start {
			line := scanner.Text()
			lineBytes := len(line) + 1 // +1 for newline
			if bytesRead+lineBytes > max {
				break
			}
			contentBuilder.WriteString(line + "\n")
			bytesRead += lineBytes
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"content": contentBuilder.String(),
	}, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise uses current working directory.
func (t *FileReadTool) resolvePath(ctx context.Context, path string) (string, error) {
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
	return "Write to a file"
}

func (t *FileWriteTool) FullDescription() string {
	return "Write content to a file. If the file already exists, it will be overwritten."
}

func (t *FileWriteTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "content"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "Relative or absolute path to the file to write",
			},
			"content": {
				Type:        "string",
				Description: "Content to write to the file",
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

// resolvePath resolves a path relative to workdir if provided in context, otherwise uses current working directory.
func (t *FileWriteTool) resolvePath(ctx context.Context, path string) (string, error) {
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
	return "Edit a file with precise text replacement"
}

func (t *FileEditTool) FullDescription() string {
	return "Edit a file by specifying exact text to find and its replacement. This allows for precise edits without needing to provide the full file content."
}

func (t *FileEditTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "edits"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "Relative or absolute path to the file to edit",
			},
			"edits": {
				Type:        "array",
				Description: "List of edits to apply, each with 'find' and 'replace' fields for exact text replacement",
				Items: &llm.Parameters{
					Type:     "object",
					Required: []string{"find", "replace"},
					Properties: map[string]llm.Property{
						"find": {
							Type:        "string",
							Description: "Exact text to find in the original file",
						},
						"replace": {
							Type:        "string",
							Description: "Text to replace the found text with",
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

	// Resolve path relative to workdir if provided in context
	resolvedPath, err := t.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	contentBytes, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %q: %w", resolvedPath, err)
	}
	content := string(contentBytes)

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

		content = strings.ReplaceAll(content, find, replace)
	}

	err = os.WriteFile(resolvedPath, []byte(content), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write file %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
	}, nil
}

// resolvePath resolves a path relative to workdir if provided in context, otherwise uses current working directory.
func (t *FileEditTool) resolvePath(ctx context.Context, path string) (string, error) {
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
