package tool

import (
	"context"
	"strings"

	"github.com/m4ns0ur/iris/internal/policy"
	apitype "github.com/m4ns0ur/iris/tool"
)

const (
	// ToolsContextKey holds the ToolRegistry in the context.
	ToolsContextKey = ContextKey("tool_registry")

	// CurrentToolContextKey holds information about the currently executing tool.
	CurrentToolContextKey = ContextKey("current_tool")

	// WorkdirContextKey holds the working directory for file operations.
	WorkdirContextKey = ContextKey("workdir")

	// PolicyContextKey is retained for backwards compatibility with internal
	// tools. New code should use the public tool environment helpers.
	PolicyContextKey = ContextKey("security_policy")
)

// ContextKey is a type for context keys.
type ContextKey string

var _ apitype.Environment = (*policy.Policy)(nil)

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

// WithPolicy attaches the security policy to a context so that custom tools
// can reuse Iris' path checks through the public tool.Environment API.
func WithPolicy(ctx context.Context, pol *policy.Policy) context.Context {
	ctx = context.WithValue(ctx, PolicyContextKey, pol)
	return apitype.WithEnvironment(ctx, pol)
}

// FromPolicy retrieves the concrete security policy from the context.
func FromPolicy(ctx context.Context) (*policy.Policy, bool) {
	if value := ctx.Value(PolicyContextKey); value != nil {
		if pol, ok := value.(*policy.Policy); ok {
			return pol, true
		}
	}

	environment, ok := apitype.FromContext(ctx)
	if !ok {
		return nil, false
	}

	pol, ok := environment.(*policy.Policy)
	return pol, ok
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
	Policy   *policy.Policy
}

// BuildPrompt adds tool descriptions to a prompt for the LLM.
func (tc *ToolContext) BuildPrompt() string {
	if tc.Registry == nil || len(tc.Registry.Tools()) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, tool := range tc.Registry.Tools() {
		sb.WriteString(tool.Name())
		sb.WriteString(": ")
		sb.WriteString(tool.Description())
		sb.WriteString("\n")
	}

	return sb.String()
}
