package tool

import (
	"context"
	"encoding/json"
	"fmt"
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

// WithTools creates a new context with the tool registry attached.
func WithTools(ctx context.Context, registry *ToolRegistry) context.Context {
	return context.WithValue(ctx, ToolsContextKey, registry)
}

// FromTools retrieves the tool registry from the context.
func FromTools(ctx context.Context) (*ToolRegistry, bool) {
	v := ctx.Value(ToolsContextKey)
	if v == nil {
		return nil, false
	}

	reg, ok := v.(*ToolRegistry)
	return reg, ok
}

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

// FromCurrentTool retrieves information about the currently executing tool.
func FromCurrentTool(ctx context.Context) (string, map[string]any, bool) {
	v := ctx.Value(CurrentToolContextKey)
	if v == nil {
		return "", nil, false
	}

	data, ok := v.(struct {
		Name string
		Args map[string]any
	})
	if !ok {
		return "", nil, false
	}

	return data.Name, data.Args, true
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

	sb.WriteString("Available Tools:\n")
	sb.WriteString("==================\n\n")

	for _, name := range tc.Registry.List() {
		tool := tc.Registry.Get(name)
		sb.WriteString(tool.Description())
		sb.WriteString("\n\nParameters:\n")

		params := tool.Parameters()
		if len(params) > 0 {
			for key, param := range params {
				paramDef := param.(map[string]any)
				desc := ""
				if d, ok := paramDef["description"].(string); ok {
					desc = " (" + d + ")"
				}
				sb.WriteString("  - " + key + desc + "\n")
			}
		}

		sb.WriteString("\n---\n\n")
	}

	return sb.String()
}

// BuildJSONPrompt creates a JSON-formatted tool schema for function calling.
func (tc *ToolContext) BuildJSONPrompt() string {
	if tc.Registry == nil || len(tc.Registry.List()) == 0 {
		return ""
	}

	var tools []map[string]any

	for _, name := range tc.Registry.List() {
		tool := tc.Registry.Get(name)
		def := ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		}

		tools = append(tools, defToMap(def))
	}

	return toJSON(tools)
}

func defToMap(d ToolDefinition) map[string]any {
	m := make(map[string]any)
	m["name"] = d.Name
	m["description"] = d.Description

	if d.Schema != nil {
		m["parameters"] = dtoSchema(d.Schema)
	} else if len(d.Parameters) > 0 {
		m["parameters"] = paramsToSchema(d.Parameters)
	}

	return m
}

func dtoSchema(s *ToolParameterSchema) map[string]any {
	if s == nil {
		return nil
	}

	result := make(map[string]any)
	result["type"] = "object"
	properties := make(map[string]any)
	requiredList := []string{}

	for name, param := range s.Properties {
		paramMap := map[string]any{
			"type":        param.Type,
			"description": param.Description,
		}
		properties[name] = paramMap

		if param.Required {
			requiredList = append(requiredList, name)
		}

		if len(param.Enum) > 0 {
			paramMap["enum"] = param.Enum
		}

		if param.Default != nil {
			paramMap["default"] = param.Default
		}
	}

	result["properties"] = properties
	if len(requiredList) > 0 {
		result["required"] = requiredList
	}

	return result
}

func paramsToSchema(params map[string]any) map[string]any {
	result := make(map[string]any)
	result["type"] = "object"
	properties := make(map[string]any)
	requiredList := []string{}

	for name, param := range params {
		paramDef := param.(map[string]any)
		paramMap := map[string]any{
			"type":        paramDef["type"],
			"description": paramDef["description"],
		}
		properties[name] = paramMap

		if req, ok := paramDef["required"]; ok && req == true {
			requiredList = append(requiredList, name)
		}
	}

	result["properties"] = properties
	if len(requiredList) > 0 {
		result["required"] = requiredList
	}

	return result
}

func toJSON(data any) string {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error marshaling to JSON: %v", err)
	}
	return string(b)
}
