//go:build ignore

package main

import (
	"context"
	"fmt"
	
	"github.com/m4ns0ur/iris/internal/tool"
)

func main() {
	fmt.Println("=== Tool Registry Example ===\n")
	
	// Create a default registry with built-in tools
	registry := tool.DefaultRegistry()
	
	fmt.Printf("Registered %d tools:\n", len(registry.List()))
	for _, name := range registry.List() {
		fmt.Printf("  - %s\n", name)
	}
	fmt.Println()
	
	// Create a custom tool
	customTool := &MyCustomTool{}
	registry.Register("custom_tool", customTool)
	fmt.Printf("Registered %d tools after adding custom tool:\n", len(registry.List()))
	fmt.Println()
	
	// Build prompt for LLM
	ctx := &tool.ToolContext{Registry: registry}
	prompt := ctx.BuildPrompt()
	fmt.Println("=== Prompt for LLM ===")
	fmt.Println(prompt)
	fmt.Println()
	
	// Execute a tool
	execCtx := context.Background()
	result, err := registry.CallWithContext(execCtx, "file_read", map[string]any{
		"path": "./README.md",
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		content := result["content"].(string)
		fmt.Printf("File content length: %d bytes\n", len(content))
	}
	fmt.Println()
	
	// Execute custom tool
	result, err = registry.CallWithContext(execCtx, "custom_tool", map[string]any{})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Custom tool result: %+v\n", result)
	}
	
	fmt.Println("\n=== Example Complete ===")
}

// MyCustomTool is an example custom tool.
type MyCustomTool struct{}

func (t *MyCustomTool) Name() string {
	return "custom_tool"
}

func (t *MyCustomTool) Description() string {
	return "A custom tool that does something useful."
}

func (t *MyCustomTool) Parameters() map[string]any {
	return nil // No parameters needed
}

func (t *MyCustomTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
	fmt.Println("Executing custom tool!")
	
	// Access current tool info from context
	name, _, ok := tool.FromCurrentTool(ctx)
	if ok {
		fmt.Printf("Running in tool: %s\n", name)
	}
	
	return map[string]any{
		"status": "success",
		"message": "Custom tool executed successfully!",
	}, nil
}
