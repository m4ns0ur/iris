// Package tool provides a registry mechanism for tools that can be used by LLM agents.
//
// The tool package includes:
//   - ToolRegistry: A central registry to manage and retrieve tools
//   - Built-in tools: read, edit, write, shell
//   - Context helpers: For passing tool information in prompts
package tool

import (
	"context"

	"github.com/m4ns0ur/iris/internal/llm"
)

// ToolRegistry manages and provides access to available tools.
type ToolRegistry struct {
	tools map[string]Tool
}

// Tool represents an executable function that can be called by the agent.
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() string

	// Description returns a human-readable description of what the tool does.
	Description() string

	// FullDescription returns a detailed description of the tool, including usage instructions.
	FullDescription() string

	// Parameters describes the input schema for this tool (optional).
	Parameters() llm.Parameters

	// Execute runs the tool with the given arguments and context.
	Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}

// NewToolRegistry creates a new empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// NewDefaultRegistry creates and registers all default built-in tools.
func NewDefaultRegistry() *ToolRegistry {
	reg := NewToolRegistry()

	// Register built-in tools
	reg.Register("read", NewFileReadTool())
	reg.Register("edit", NewFileEditTool())
	reg.Register("write", NewFileWriteTool())
	reg.Register("shell", NewShellTool())

	return reg
}

// Register adds a tool to the registry. Panics if name already exists.
func (r *ToolRegistry) Register(name string, t Tool) {
	if r.tools[name] != nil {
		panic("tool already registered: " + name)
	}
	r.tools[name] = t
}

// Get retrieves a tool by name. Returns nil if not found.
func (r *ToolRegistry) Get(name string) Tool {
	return r.tools[name]
}

// List returns names of all registered tools.
func (r *ToolRegistry) List() []llm.Tool {
	tools := make([]llm.Tool, 0, len(r.tools))
	for name, tool := range r.tools {
		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.Function{
				Name:        name,
				Description: tool.FullDescription(),
				Parameters:  tool.Parameters(),
			},
		})
	}
	return tools
}

// ExecuteWithTools is a helper to execute a tool with proper context.
func ExecuteWithTools(ctx context.Context, registry *ToolRegistry, name string, args map[string]any) (map[string]any, error) {
	tool := registry.Get(name)
	if tool == nil {
		return nil, ErrToolNotFound
	}

	ctx = WithCurrentTool(ctx, name, args)
	return tool.Execute(ctx, args)
}
