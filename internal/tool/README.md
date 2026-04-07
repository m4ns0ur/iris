# Tool Registry Package

A simple tool registry mechanism designed to be used by LLM agents. This package provides a centralized way to manage, register, and execute tools that an agent can use.

## Overview

The tool registry allows you to:
- Register custom tools with metadata (name, description, parameters)
- Retrieve tools by name
- Execute tools with proper context management
- Generate prompts for LLMs describing available tools

## Built-in Tools

The built-in registry includes the following tools:
- **file_read**: Read the contents of a file
- **file_write**: Write content to a file (creates or overwrites)
- **search**: Find files matching glob patterns
- **grep**: Search for text patterns in files
- **shell**: Execute shell commands

## Usage

### Basic Setup

```go
import "github.com/m4ns0ur/iris/internal/tool"

// Create a default registry with built-in tools
registry := tool.DefaultRegistry()

// Or create an empty registry and add custom tools
registry = tool.NewToolRegistry()
```

### Registering Custom Tools

```go
type MyCustomTool struct{}

func (t *MyCustomTool) Name() string { return "my_tool" }
func (t *MyCustomTool) Description() string { return "Does something useful" }
func (t *MyCustomTool) Parameters() map[string]any {
    return map[string]any{
        "param1": map[string]any{
            "type":        "string",
            "description": "Description of param1",
            "required":    true,
        },
    }
}
func (t *MyCustomTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
    // Implement your logic here
    return map[string]any{"result": "success"}, nil
}

// Register the tool
registry.Register("my_tool", &MyCustomTool{})
```

### Executing Tools

```go
// Using CallWithContext
ctx := context.Background()
result, err := registry.CallWithContext(ctx, "file_read", map[string]any{
    "path": "/path/to/file.txt",
})

if err != nil {
    // Handle error
}

// Or using helper functions
result, err = tool.ExecuteWithTools(ctx, registry, "file_read", map[string]any{
    "path": "/path/to/file.txt",
})
```

### Generating Prompts for LLMs

```go
// Build a natural language description of available tools
toolContext := &tool.ToolContext{Registry: registry}
prompt := toolContext.BuildPrompt()
// Returns something like:
// Available Tools:
// ==================
//
// Read the contents of a file...
//
// Parameters:
//   - path (Path to the file to read)
//
// ---
// ...

// Or generate JSON schema for function calling
jsonSchema := toolContext.BuildJSONPrompt()
```

### Context Management

The package provides context keys for passing information between tool executions:

```go
import "github.com/m4ns0ur/iris/internal/tool"

// Get the registry from context
registry, ok := tool.FromTools(ctx)
if !ok {
    // Handle missing context
}

// Get current tool info
name, args, ok := tool.FromCurrentTool(ctx)
```

## Tool Interface

All tools must implement the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() map[string]any
    Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}
```

### Methods

- **Name()**: Returns a unique identifier for the tool
- **Description()**: Human-readable description of what the tool does
- **Parameters()**: Optional parameter schema for the tool
- **Execute(ctx, args)**: Runs the tool with given arguments and context

## Example: Complete Workflow

```go
package main

import (
    "context"
    "fmt"

    "github.com/m4ns0ur/iris/internal/tool"
)

func main() {
    // Create registry
    registry := tool.DefaultRegistry()

    // Add custom tool
    registry.Register("custom_tool", &MyTool{})

    // Execute a tool
    ctx := context.Background()
    result, err := registry.CallWithContext(ctx, "file_read", map[string]any{
        "path": "./README.md",
    })

    if err != nil {
        fmt.Printf("Error: %v\n", err)
        return
    }

    content := result["content"].(string)
    fmt.Println(content)
}

type MyTool struct{}

func (t *MyTool) Name() string { return "custom_tool" }
func (t *MyTool) Description() string { return "A custom tool for special tasks" }
func (t *MyTool) Parameters() map[string]any {
    return nil // No parameters needed
}
func (t *MyTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
    fmt.Println("Custom tool executed!")
    return map[string]any{"status": "done"}, nil
}
```

## Error Handling

The package defines several error types:

- `tool.ErrToolNotFound`: Tool name doesn't exist in registry
- `tool.ErrInvalidArgs`: Invalid or missing arguments for a tool
- `tool.ErrExecutionFailed`: General execution failure

Check errors after calling any tool method to handle failures appropriately.

## Security Notes

When using tools like `shell`, be cautious about:
- Command injection vulnerabilities
- Running commands with elevated privileges
- Accessing sensitive files or directories

Consider implementing additional validation and filtering for untrusted inputs.
