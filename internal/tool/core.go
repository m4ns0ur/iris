// Package tool provides the built-in tools and their registry.
package tool

import (
	"context"
	"fmt"

	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/provider"
	apitype "github.com/m4ns0ur/iris/tool"
)

// Tool is the public tool contract.
type Tool = apitype.Tool

var _ apitype.Registry = (*ToolRegistry)(nil)

// ToolRegistry manages and provides access to available tools.
type ToolRegistry struct {
	tools  map[string]Tool
	temp   *TempStore
	policy *policy.Policy
	output outputPolicy
}

// NewToolRegistry creates a new empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// NewDefaultRegistry creates and registers all default built-in tools. Every
// file and shell tool is bound to the supplied security policy. Additional
// tools are registered after the built-ins.
func NewDefaultRegistry(pol *policy.Policy, extra ...Tool) (*ToolRegistry, error) {
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
	registry.policy = pol
	registry.output = newOutputPolicy(pol.ShellOptions())

	registry.Register("read", NewFileReadTool(pol))
	registry.Register("edit", NewFileEditTool(pol))
	registry.Register("write", NewFileWriteTool(pol))
	registry.Register("shell", NewShellTool(pol, temp))

	for _, extraTool := range extra {
		if extraTool == nil {
			continue
		}
		if extraTool.Name() == "" {
			_ = temp.Close()
			return nil, fmt.Errorf("tool name must not be empty")
		}
		if registry.Get(extraTool.Name()) != nil {
			_ = temp.Close()
			return nil, fmt.Errorf("tool already registered: %s", extraTool.Name())
		}
		registry.Register(extraTool.Name(), extraTool)
	}

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

// Tools returns the registered tools.
func (r *ToolRegistry) Tools() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	return tools
}

// Specs returns the provider-facing definitions of the registered tools.
func (r *ToolRegistry) Specs() []provider.Tool {
	tools := make([]provider.Tool, 0, len(r.tools))
	for name, t := range r.tools {
		tools = append(tools, provider.Tool{
			Type: "function",
			Function: provider.Function{
				Name:        name,
				Description: t.FullDescription(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return tools
}

// SetOutputDir redirects full tool output to a persistent directory and grants
// the read tool access to it. The client/TUI use the session tool-output
// directory after loading a session.
func (r *ToolRegistry) SetOutputDir(dir string) error {
	if r == nil || r.temp == nil {
		return fmt.Errorf("tool registry has no output store")
	}
	if r.policy == nil {
		return fmt.Errorf("tool registry has no security policy")
	}
	if err := r.temp.SetDir(dir); err != nil {
		return err
	}
	if err := r.policy.AddReadRoot(dir); err != nil {
		return err
	}
	return nil
}

// TempDir returns the directory used for full tool output.
func (r *ToolRegistry) TempDir() string {
	if r == nil || r.temp == nil {
		return ""
	}
	return r.temp.Dir()
}

// Close releases resources owned by the registry. Ephemeral tool output is
// removed; a persistent session tool-output directory is kept.
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

	result, err = t.Execute(ctx, args)
	if err != nil || result == nil {
		return result, err
	}
	return registry.applyOutput(name, result), nil
}
