package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

const (
	defaultReadLines = 1000
	defaultReadBytes = 25 * 1024
	maxReadLines     = 10_000
)

// FileReadTool reads the contents of a file inside the workspace.
type FileReadTool struct {
	policy *policy.Policy
}

func NewFileReadTool(pol *policy.Policy) *FileReadTool {
	return &FileReadTool{policy: pol}
}

func (t *FileReadTool) Name() string {
	return "read"
}

func (t *FileReadTool) Description() string {
	return "Read a file"
}

func (t *FileReadTool) FullDescription() string {
	return "Read a file inside the workspace. Output is limited to 1000 lines or 25KB; use start/max to read parts. Paths outside the workspace are rejected."
}

func (t *FileReadTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path (absolute or relative to the workspace)",
			},
			"start": {
				Type:        "number",
				Description: "Starting line (1-based)",
			},
			"max": {
				Type:        "number",
				Description: "Maximum lines to read (default 1000)",
			},
		},
	}
}

func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	if t.policy == nil {
		return nil, fmt.Errorf("%w: read tool has no security policy", ErrExecutionFailed)
	}

	resolvedPath, err := t.policy.ResolveRead(path)
	if err != nil {
		return nil, err
	}

	start := 1
	if value, ok := args["start"].(float64); ok && value >= 1 {
		start = int(value)
	}

	maxLines := defaultReadLines
	if value, ok := args["max"].(float64); ok && value > 0 {
		maxLines = int(value)
		if maxLines > maxReadLines {
			maxLines = maxReadLines
		}
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file %q: %w", resolvedPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %q", resolvedPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file: %q", resolvedPath)
	}

	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %q: %w", resolvedPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var content strings.Builder
	line := 0
	linesRead := 0
	bytesRead := 0
	truncated := false

	for scanner.Scan() {
		line++
		if line < start {
			if line%512 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			continue
		}

		text := scanner.Text()
		needed := len(text) + 1

		if linesRead >= maxLines || bytesRead+needed > defaultReadBytes {
			truncated = true
			break
		}

		content.WriteString(text)
		content.WriteByte('\n')
		linesRead++
		bytesRead += needed

		if linesRead%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read error %q: %w", resolvedPath, err)
	}

	result := map[string]any{
		"content":    content.String(),
		"truncated":  truncated,
		"start_line": start,
		"lines":      linesRead,
	}
	if linesRead > 0 {
		result["end_line"] = start + linesRead - 1
	}

	return result, nil
}
