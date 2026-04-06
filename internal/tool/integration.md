# Tool Registry Integration Guide

This guide shows how to integrate the tool registry with your LLM agent.

## Overview

The tool registry provides a centralized mechanism for managing tools that an LLM agent can use. It includes:

1. **Tool Registration**: Register custom tools with metadata
2. **Prompt Generation**: Generate natural language or JSON prompts describing available tools
3. **Tool Execution**: Execute tools safely with proper context management

## Integration Steps

### Step 1: Create a Tool Registry

```go
import "github.com/m4ns0ur/iris/internal/tool"

// Option 1: Use default registry with built-in tools
registry := tool.DefaultRegistry()

// Option 2: Create empty registry and add custom tools
registry = tool.NewToolRegistry()
registry.Register("my_tool", &MyCustomTool{})
```

### Step 2: Generate Tool Prompts for LLM

Add tool descriptions to your system prompt so the LLM knows what tools are available.

#### Natural Language Prompt

```go
toolContext := &tool.ToolContext{Registry: registry}
prompt := toolContext.BuildPrompt()
// Returns human-readable description of all tools
```

Example output:
```
Available Tools:
==================

Read the contents of a file...

Parameters:
  - path (Path to the file to read)

---

Write content to a file...
```

#### JSON Schema for Function Calling

```go
jsonSchema := toolContext.BuildJSONPrompt()
// Returns JSON schema compatible with function calling APIs
```

### Step 3: Execute Tools

When the LLM decides to use a tool, execute it through the registry.

```go
ctx := context.Background()

result, err := registry.CallWithContext(ctx, "file_read", map[string]any{
    "path": "/path/to/file.txt",
})

if err != nil {
    // Handle error
}

content := result["content"].(string)
```

### Step 4: Integrate with Agent Loop

Here's how to integrate the tool registry into your agent loop:

```go
type Agent struct {
    client      *llm.LLMClient
    registry    *tool.ToolRegistry
    // ... other fields
}

func NewAgent(client *llm.LLMClient) *Agent {
    return &Agent{
        client:   client,
        registry: tool.DefaultRegistry(), // or create custom registry
        // ...
    }
}

// In your runTask method
func (a *Agent) runTask() error {
    task := a.goals[a.currentTask]
    
    // Get available tools for this step
    toolContext := &tool.ToolContext{Registry: a.registry}
    
    // Add to prompt
    systemPrompt := prompt.BuildSystemPrompt() + "\n\n" + toolContext.BuildPrompt()
    
    // Generate response from LLM
    opts := &llm.LLMOptions{Temperature: 0.1}
    resp, err := a.client.Generate(systemPrompt, task, opts)
    if err != nil {
        return err
    }
    
    // Parse tool call from response
    toolCall, args := a.parseToolCall(resp)
    if toolCall == "" {
        return nil // No tool needed
    }
    
    // Execute the tool
    ctx := context.Background()
    result, err := a.registry.CallWithContext(ctx, toolCall, args)
    if err != nil {
        return fmt.Errorf("tool execution failed: %w", err)
    }
    
    // Add result to conversation history
    a.history = append(a.history, Message{
        Role:    "assistant",
        Content: fmt.Sprintf("Tool '%s' returned: %+v", toolCall, result),
    })
    
    return nil
}

func (a *Agent) parseToolCall(response string) (string, map[string]any) {
    // Parse the LLM's response to extract tool name and arguments
    // This depends on your prompt format and parsing strategy
    return "", nil
}
```

## Advanced Integration Patterns

### Pattern 1: Tool Selection with Reasoning

Encourage the LLM to reason about which tool to use:

```go
toolPrompt := fmt.Sprintf(`
Available Tools:
%s

Task: %s

Which tool should I use and why? Consider the task requirements.
`, toolContext.BuildPrompt(), task)

response, _ := a.client.Generate(systemPrompt, toolPrompt, opts)
// Parse reasoning and tool selection from response
```

### Pattern 2: Tool Chain Execution

For complex tasks, chain multiple tools together:

```go
func (a *Agent) executeToolChain(task string) error {
    // Step 1: Search for relevant files
    searchResult, _ := a.registry.CallWithContext(ctx, "search", map[string]any{
        "pattern": "*.md",
    })
    
    // Step 2: Read the most relevant file
    if matches, ok := searchResult["matches"].([]string); ok && len(matches) > 0 {
        readResult, _ := a.registry.CallWithContext(ctx, "file_read", map[string]any{
            "path": matches[0],
        })
        
        // Step 3: Process the content...
    }
    
    return nil
}
```

### Pattern 3: Error Handling and Retry

Implement robust error handling for tool execution:

```go
func (a *Agent) executeToolWithRetry(toolName string, args map[string]any, maxRetries int) (map[string]any, error) {
    var lastErr error
    
    for i := 0; i < maxRetries; i++ {
        result, err := a.registry.CallWithContext(ctx, toolName, args)
        
        if err == nil {
            return result, nil
        }
        
        lastErr = err
        
        // Log the error and retry with adjusted parameters
        Logger.Warn("Tool execution failed, will retry", "attempt", i+1, "error", err)
    }
    
    return nil, fmt.Errorf("tool '%s' failed after %d attempts: %w", toolName, maxRetries, lastErr)
}
```

## Security Considerations

### 1. Input Validation

Always validate tool arguments before execution:

```go
func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
    path, ok := args["path"].(string)
    if !ok || path == "" {
        return nil, fmt.Errorf("missing required parameter: path")
    }
    
    // Validate path is within allowed directory
    allowedDir := "/allowed/path"
    if !strings.HasPrefix(filepath.Clean(path), allowedDir) {
        return nil, fmt.Errorf("path outside allowed directory")
    }
    
    content, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read file: %w", err)
    }
    
    return map[string]any{"content": string(content)}, nil
}
```

### 2. Command Filtering for Shell Tool

The built-in shell tool already filters dangerous commands:

```go
dangerousPatterns := []string{
    "rm -rf", "mkfs", "dd if=", ":(){:|:&}", "/bin/sh -c", "/bin/bash -c",
}
for _, pattern := range dangerousPatterns {
    if strings.Contains(command, pattern) {
        return nil, fmt.Errorf("command rejected for security reasons")
    }
}
```

### 3. Rate Limiting

Consider implementing rate limiting for tool execution:

```go
type RateLimitedRegistry struct {
    registry *tool.ToolRegistry
    mu       sync.Mutex
    calls    map[string]int
}

func (r *RateLimitedRegistry) CallWithContext(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    r.calls[name]++
    if r.calls[name] > 10 { // Limit to 10 calls per tool
        return nil, fmt.Errorf("rate limit exceeded for tool: %s", name)
    }
    
    return r.registry.CallWithContext(ctx, name, args)
}
```

## Testing Tools

The package includes a test example. Run it with:

```bash
cd internal/tool
go run example.go
```

This demonstrates:
- Creating and registering tools
- Building prompts for LLMs
- Executing built-in and custom tools
- Using context to access tool information

## Migration from Legacy System

If you're migrating from a previous system without a registry:

1. **Identify existing tools**: List all tools your agent currently uses
2. **Create Tool implementations**: Implement the `Tool` interface for each
3. **Register in registry**: Add them to a `ToolRegistry`
4. **Update prompts**: Use `BuildPrompt()` or `BuildJSONPrompt()` instead of manual descriptions
5. **Centralize execution**: Route all tool calls through the registry

Example migration:

```go
// Before (scattered tool implementations)
func (a *Agent) readFile(path string) { /* ... */ }
func (a *Agent) writeFile(path, content string) { /* ... */ }

// After (centralized via registry)
registry := tool.NewToolRegistry()
registry.Register("file_read", &FileReadTool{})
registry.Register("file_write", &FileWriteTool{})

// Execute through registry
result, _ := registry.CallWithContext(ctx, "file_read", map[string]any{"path": path})
```

## Troubleshooting

### Tool Not Found Error

If you get `tool.ErrToolNotFound`:
- Verify the tool name matches exactly (case-sensitive)
- Check that the tool is registered before use
- Use `registry.List()` to see all registered tools

### Invalid Arguments Error

If you get `tool.ErrInvalidArgs`:
- Check parameter names match the tool's schema
- Ensure required parameters are provided
- Validate data types match expectations

### Context Issues

When using context helpers:
```go
// Always set context before calling tools
ctx := WithCurrentTool(context.Background(), "file_read", args)
result, _ := registry.CallWithContext(ctx, "file_read", args)

// Retrieve from context if needed
name, _, ok := tool.FromCurrentTool(ctx)
if !ok {
    // Context not set properly
}
```

## Next Steps

1. Review the [README.md](./README.md) for detailed API documentation
2. Study the example in `example.go` for usage patterns
3. Implement custom tools specific to your use case
4. Integrate with your agent's planning and execution loop
5. Add security validations appropriate for your environment
