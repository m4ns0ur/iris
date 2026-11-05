// Package tool provides a registry mechanism for tools that can be used by LLM agents.
//
// The tool package includes:
//   - ToolRegistry: A central registry to manage and retrieve tools
//   - Built-in tools: read, edit, write, shell
//   - Context helpers: For passing tool information in prompts
package tool

import (
	"context"
	"fmt"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

// ToolRegistry manages and provides access to available tools.
type ToolRegistry struct {
	tools map[string]Tool
	temp  *TempStore
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

// NewDefaultRegistry creates and registers all default built-in tools. Every
// file and shell tool is bound to the supplied security policy.
func NewDefaultRegistry(pol *policy.Policy) (*ToolRegistry, error) {
	if pol == nil {
		return nil, fmt.Errorf("%w: tool registry requires a security policy", ErrExecutionFailed)
	}

	registry := NewToolRegistry()

	temp, err := NewTempStore(pol.ShellOptions().TempDirTTL)
	if err != nil {
		return nil, fmt.Errorf("create shell output store: %w", err)
	}

	// The read tool may inspect captured shell output, but nothing else in
	// the temp directory is writable.
	if err := pol.AddReadRoot(temp.Dir()); err != nil {
		_ = temp.Close()
		return nil, fmt.Errorf("grant read access to shell output store: %w", err)
	}

	registry.temp = temp

	registry.Register("read", NewFileReadTool(pol))
	registry.Register("edit", NewFileEditTool(pol))
	registry.Register("write", NewFileWriteTool(pol))
	registry.Register("shell", NewShellTool(pol, temp))

	return registry, nil
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
	for name, t := range r.tools {
		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.Function{
				Name:        name,
				Description: t.FullDescription(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return tools
}

// TempDir returns the directory used for captured shell output.
func (r *ToolRegistry) TempDir() string {
	if r == nil || r.temp == nil {
		return ""
	}
	return r.temp.Dir()
}

// Close releases resources owned by the registry, including captured output
// files created by the shell tool.
func (r *ToolRegistry) Close() error {
	if r == nil || r.temp == nil {
		return nil
	}
	return r.temp.Close()
}

// ExecuteWithTools is a helper to execute a tool with proper context. Tool
// panics are converted into errors so that a buggy tool cannot crash the
// agent.
func ExecuteWithTools(
	ctx context.Context,
	registry *ToolRegistry,
	name string,
	args map[string]any,
) (result map[string]any, err error) {
	t := registry.Get(name)
	if t == nil {
		return nil, ErrToolNotFound
	}

	ctx = WithCurrentTool(ctx, name, args)

	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("%w: tool %q panicked: %v", ErrExecutionFailed, name, recovered)
		}
	}()

	return t.Execute(ctx, args)
}
