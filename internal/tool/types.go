package tool

import (
	"context"
)

// Tool represents an executable function that can be called by the agent.
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() string

	// Description returns a human-readable description of what the tool does.
	Description() string

	// Parameters describes the input schema for this tool (optional).
	Parameters() map[string]any

	// Execute runs the tool with the given arguments and context.
	Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}

// ToolDefinition holds metadata about a tool for LLM prompts.
type ToolDefinition struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]any       `json:"parameters,omitempty"`
	Schema      *ToolParameterSchema `json:"schema,omitempty"`
}

// ToolParameterSchema defines the parameter schema for a tool.
type ToolParameterSchema struct {
	Type        string            `json:"type"`
	Properties  map[string]*Param `json:"properties,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Description string            `json:"description,omitempty"`
}

// Param defines a single parameter in the tool schema.
type Param struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ToolRegistry manages and provides access to available tools.
type ToolRegistry struct {
	tasks map[string]Tool
}

// NewToolRegistry creates a new empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tasks: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. Panics if name already exists.
func (r *ToolRegistry) Register(name string, t Tool) {
	if r.tasks[name] != nil {
		panic("tool already registered: " + name)
	}
	r.tasks[name] = t
}

// Get retrieves a tool by name. Returns nil if not found.
func (r *ToolRegistry) Get(name string) Tool {
	return r.tasks[name]
}

// GetAll returns all registered tools as definitions.
func (r *ToolRegistry) GetAll() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(r.tasks))
	for name, tool := range r.tasks {
		defs = append(defs, ToolDefinition{
			Name:        name,
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return defs
}

// Has checks if a tool with the given name exists.
func (r *ToolRegistry) Has(name string) bool {
	return r.tasks[name] != nil
}

// List returns names of all registered tools.
func (r *ToolRegistry) List() []string {
	names := make([]string, 0, len(r.tasks))
	for name := range r.tasks {
		names = append(names, name)
	}
	return names
}
