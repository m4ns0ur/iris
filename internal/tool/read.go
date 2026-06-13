package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
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
	return "Read a file. Output is limited to 1000 lines or 25KB; use start/max to read parts. Continue reading offsets until the full file is obtained."
}

func (t *FileReadTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path",
			},
			"start": {
				Type:        "number",
				Description: "Starting line (1-based)",
			},
			"max": {
				Type:        "number",
				Description: "Maximum lines to read",
			},
		},
	}
}

func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	resolvedPath, err := resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}

	start := 1
	if s, ok := args["start"].(float64); ok && s > 0 {
		start = int(s)
	}

	maxBytes := 25 * 1024
	if m, ok := args["max"].(float64); ok && m > 0 {
		maxBytes = int(m)
	}

	const maxLines = 1000

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

	var b strings.Builder
	line := 1
	readBytes := 0
	readLines := 0

	for scanner.Scan() {
		if line >= start {
			txt := scanner.Text()
			n := len(txt) + 1 // include newline

			if readBytes+n > maxBytes || readLines >= maxLines {
				break
			}

			b.WriteString(txt)
			b.WriteByte('\n')

			readBytes += n
			readLines++
		}
		line++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read error %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"content": b.String(),
	}, nil
}
