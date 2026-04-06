// Package tool provides a registry mechanism for tools that can be used by LLM agents.
//
// The tool package includes:
//   - ToolRegistry: A central registry to manage and retrieve tools
//   - Built-in tools: file_read, file_write, file_list, shell, search, grep
//   - Context helpers: For passing tool information in prompts
package tool

import (
	"context"
)

// DefaultRegistry returns a pre-configured registry with built-in tools.
func DefaultRegistry() *ToolRegistry {
	return NewDefaultRegistry()
}

// NewDefaultRegistry creates and registers all default built-in tools.
func NewDefaultRegistry() *ToolRegistry {
	reg := NewToolRegistry()
	
	// Register file operations
	reg.Register("file_read", NewFileReadTool())
	reg.Register("file_write", NewFileWriteTool())
	reg.Register("file_list", NewFileListTool())
	
	// Register system tools
	reg.Register("shell", NewShellTool())
	reg.Register("search", NewSearchTool())
	reg.Register("grep", NewGrepTool())
	
	return reg
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

// ExecuteWithToolsAndContext is a helper to execute a tool with context and proper error handling.
func ExecuteWithToolsAndContext(ctx context.Context, registry *ToolRegistry, name string, args map[string]any) (map[string]any, error) {
	tool := registry.Get(name)
	if tool == nil {
		return nil, ErrToolNotFound
	}
	
	ctx = WithCurrentTool(ctx, name, args)
	result, err := tool.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	
	return result, nil
}

// Call executes a tool and returns the result.
func (r *ToolRegistry) Call(name string, args map[string]any) (map[string]any, error) {
	tool := r.Get(name)
	if tool == nil {
		return nil, ErrToolNotFound
	}
	
	ctx := context.Background()
	return tool.Execute(ctx, args)
}

// CallWithContext executes a tool with the given context.
func (r *ToolRegistry) CallWithContext(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	tool := r.Get(name)
	if tool == nil {
		return nil, ErrToolNotFound
	}
	
	ctx = WithCurrentTool(ctx, name, args)
	return tool.Execute(ctx, args)
}
