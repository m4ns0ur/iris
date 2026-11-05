package tool

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

// FileEditTool makes precise edits to a file based on exact text replacement.
type FileEditTool struct {
	policy *policy.Policy
}

func NewFileEditTool(pol *policy.Policy) *FileEditTool {
	return &FileEditTool{policy: pol}
}

func (t *FileEditTool) Name() string {
	return "edit"
}

func (t *FileEditTool) Description() string {
	return "Edit a file"
}

func (t *FileEditTool) FullDescription() string {
	return "Edit a file via exact text replacements. Each find must match exactly once in the original file and edits must not overlap. Merge related changes into a single edit. Writes are atomic."
}

func (t *FileEditTool) Parameters() llm.Parameters {
	return llm.Parameters{
		Type:     "object",
		Required: []string{"path", "edits"},
		Properties: map[string]llm.Property{
			"path": {
				Type:        "string",
				Description: "File path (absolute or relative to the workspace)",
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
							Description: "Exact text that must occur exactly once",
						},
						"replace": {
							Type:        "string",
							Description: "Replacement text (may be empty)",
						},
					},
				},
			},
		},
	}
}

type textReplacement struct {
	start   int
	end     int
	find    string
	replace string
}

func (t *FileEditTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	edits, ok := args["edits"].([]any)
	if !ok || len(edits) == 0 {
		return nil, fmt.Errorf("missing required parameter: edits")
	}

	if t.policy == nil {
		return nil, fmt.Errorf("%w: edit tool has no security policy", ErrExecutionFailed)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolvedPath, err := t.policy.ResolveWrite(path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("stat failed %q: %w", resolvedPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %q", resolvedPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file: %q", resolvedPath)
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("read failed %q: %w", resolvedPath, err)
	}

	content := string(data)
	replacements := make([]textReplacement, 0, len(edits))

	for index, raw := range edits {
		editMap, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid edit format at index %d", index)
		}

		find, ok := editMap["find"].(string)
		if !ok || find == "" {
			return nil, fmt.Errorf("missing required field in edit %d: find", index+1)
		}

		replace, ok := editMap["replace"].(string)
		if !ok {
			return nil, fmt.Errorf("missing required field in edit %d: replace", index+1)
		}

		count := strings.Count(content, find)
		if count == 0 {
			return nil, fmt.Errorf("edit %d: text to find was not found in the file: %q", index+1, find)
		}
		if count > 1 {
			return nil, fmt.Errorf(
				"edit %d: find text must match exactly once, but %q occurs %d times; include more surrounding context to make it unique",
				index+1,
				find,
				count,
			)
		}

		start := strings.Index(content, find)
		replacements = append(replacements, textReplacement{
			start:   start,
			end:     start + len(find),
			find:    find,
			replace: replace,
		})
	}

	sort.Slice(replacements, func(i, j int) bool {
		if replacements[i].start == replacements[j].start {
			return replacements[i].end < replacements[j].end
		}
		return replacements[i].start < replacements[j].start
	})

	for i := 1; i < len(replacements); i++ {
		if replacements[i].start < replacements[i-1].end {
			return nil, fmt.Errorf(
				"edits overlap: %q and %q both match inside the same region; merge them into one edit",
				replacements[i-1].find,
				replacements[i].find,
			)
		}
	}

	var builder strings.Builder
	last := 0
	for _, replacement := range replacements {
		builder.WriteString(content[last:replacement.start])
		builder.WriteString(replacement.replace)
		last = replacement.end
	}
	builder.WriteString(content[last:])

	updated := builder.String()
	if updated == content {
		return map[string]any{
			"success": true,
			"edits":   len(replacements),
			"changed": false,
		}, nil
	}

	// Re-resolve immediately before writing. If the path now points somewhere
	// else (for example a symlink was swapped), refuse rather than writing the
	// edited content to an unexpected location.
	freshPath, err := t.policy.ResolveWrite(path)
	if err != nil {
		return nil, err
	}
	if freshPath != resolvedPath {
		return nil, fmt.Errorf("path changed while editing (%q -> %q); retry the edit", resolvedPath, freshPath)
	}

	if err := writeFileAtomic(resolvedPath, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("write failed %q: %w", resolvedPath, err)
	}

	return map[string]any{
		"success": true,
		"edits":   len(replacements),
		"changed": true,
	}, nil
}
