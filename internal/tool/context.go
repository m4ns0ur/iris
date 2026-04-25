package tool

import (
	"context"
	"strings"
)

const (
	// ToolsContextKey holds the ToolRegistry in the context.
	ToolsContextKey = ContextKey("tool_registry")

	// CurrentToolContextKey holds information about the currently executing tool.
	CurrentToolContextKey = ContextKey("current_tool")

	// WorkdirContextKey holds the working directory for file operations.
	WorkdirContextKey = ContextKey("workdir")
)

// ContextKey is a type for context keys.
type ContextKey string

// WithWorkdir creates a new context with the working directory set.
func WithWorkdir(ctx context.Context, workdir string) context.Context {
	return context.WithValue(ctx, WorkdirContextKey, workdir)
}

// FromWorkdir retrieves the working directory from the context.
func FromWorkdir(ctx context.Context) (string, bool) {
	v := ctx.Value(WorkdirContextKey)
	if v == nil {
		return "", false
	}

	workdir, ok := v.(string)
	return workdir, ok
}

// WithCurrentTool creates a new context with information about the currently executing tool.
func WithCurrentTool(ctx context.Context, name string, args map[string]any) context.Context {
	return context.WithValue(ctx, CurrentToolContextKey, struct {
		Name string
		Args map[string]any
	}{Name: name, Args: args})
}

// ToolContext is a helper struct for passing tool-related information in prompts.
type ToolContext struct {
	Registry *ToolRegistry
	Workdir  string
}

// BuildPrompt adds tool descriptions to a prompt for the LLM.
func (tc *ToolContext) BuildPrompt() string {
	if tc.Registry == nil || len(tc.Registry.List()) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, tool := range tc.Registry.List() {
		sb.WriteString(tool.Function.Name + ": ")
		sb.WriteString(tool.Function.Description)
		sb.WriteString("\n")
	}

	return sb.String()
}
