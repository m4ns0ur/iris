package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/prompt"
	"github.com/m4ns0ur/iris/internal/tool"
)

var (
	Logger      = logger.Logger
	toolContext *tool.ToolContext
)

type Agent struct {
	client      *llm.LLMClient
	state       string
	step        int
	goals       []string
	currentTask int
	history     []llm.ChatMessage
	maxSteps    int
	workdir     string
	toolName    string
	toolArgs    map[string]any
}

func NewAgent(client *llm.LLMClient, workdir string) *Agent {
	registry := tool.NewDefaultRegistry()
	toolContext = &tool.ToolContext{Registry: registry, Workdir: workdir}

	return &Agent{
		client:      client,
		state:       "idle",
		goals:       []string{},
		currentTask: -1,
		history:     []llm.ChatMessage{},
		maxSteps:    50, // Prevent infinite loops
		workdir:     workdir,
	}
}

// Run executes the agentic loop with streaming support
func (a *Agent) Run(input string, onChunk func(chunkType string, content string), onError func(error)) error {
	a.reset()
	// Initialize with system prompt and user input
	a.history = append(a.history, llm.ChatMessage{Role: "system", Content: prompt.BuildSystemPrompt(toolContext)})
	a.history = append(a.history, llm.ChatMessage{Role: "user", Content: input})
	return a.loop(onChunk, onError)
}

// RunSync is a convenience method for synchronous execution (backward compatible)
func (a *Agent) RunSync(input string) error {
	return a.Run(input, nil, nil)
}

func (a *Agent) reset() {
	a.state = "idle"
	a.step = 0
	a.goals = []string{}
	a.currentTask = -1
	a.history = []llm.ChatMessage{}
}

func (a *Agent) loop(onChunk func(chunkType string, content string), onError func(error)) error {
	registry := tool.NewDefaultRegistry()

	for a.step < a.maxSteps {
		Logger.Debug("Agent Loop", "step", a.step, "state", a.state, "currentTask", a.currentTask)

		onChunk("next", fmt.Sprintf("%d", a.step))

		// Decide next action based on current state and progress
		err := a.next(onChunk, onError)
		if err != nil {
			return err
		}

		switch a.state {
		case "respond":
			// In respond state, we just wait for the next user input or completion
			Logger.Debug("Agent is responding to user")
			return nil

		case "tool":
			// Execute the tool with context including workdir
			ctx := context.Background()
			ctx = tool.WithWorkdir(ctx, a.workdir)
			result, err := tool.ExecuteWithTools(ctx, registry, a.toolName, a.toolArgs)
			var resultStr string
			if err == nil {
				resultStr = formatResult(result)
			} else {
				resultStr = fmt.Sprintf("failed: %v", err)
			}

			Logger.Debug("Tool result", "toolName", a.toolName, "result", resultStr)

			// Add result to conversation history
			a.history = append(a.history, llm.ChatMessage{
				Role:    "tool",
				Content: fmt.Sprintf("'%s': %s", a.toolName, resultStr),
			})

		case "done":
			Logger.Debug("Agent loop completed")
			return nil

		default:
			Logger.Warn("Unknown state", "state", a.state)
			a.state = "idle"
		}

		if a.step >= a.maxSteps {
			Logger.Warn("Max steps reached")
			break
		}

		a.step++
	}

	return nil
}

func (a *Agent) next(onChunk func(chunkType string, content string), onError func(error)) error {
	Logger.Debug("Decision phase", "history", a.history)

	tools := toolContext.Registry.List()
	chunk, err := a.client.Generate(a.history, tools, nil, onChunk, onError)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	Logger.Debug("next: LLM response received", "response", chunk)

	if chunk.Choices[0].FinishReason == "stop" {
		a.state = "done"
		a.history = append(a.history, llm.ChatMessage{Role: "assistant", Content: chunk.Choices[0].Delta.Content})
		return nil
	}
	if chunk.Choices[0].Delta.ToolCalls != nil {
		function := chunk.Choices[0].Delta.ToolCalls[0]
		Logger.Debug("Tool call decision", "function", function)
		a.state = "tool"
		a.toolName = function.Function.Name
		if function.Function.Arguments != "" {
			Logger.Debug("Function arguments", "arguments", function.Function.Arguments)
			var argsMap map[string]any
			err := json.Unmarshal([]byte(function.Function.Arguments), &argsMap)
			if err != nil {
				Logger.Error("Failed to parse tool arguments, using raw string", "error", err)
				// TODO: handle error
			}
			a.toolArgs = argsMap
		}
		Logger.Debug("Tool execution decision", "toolName", a.toolName, "toolArgs", a.toolArgs)
	}

	return nil
}

func formatResult(result map[string]any) string {
	if result == nil {
		return "(no output)"
	}

	content, ok := result["content"].(string)
	if ok && content != "" {
		return truncateString(content, 500)
	}

	// Try to marshal as JSON if not a simple string
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		return string(jsonBytes[:min(len(jsonBytes), 1000)]) + "..."
	}

	return fmt.Sprintf("%v", result)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
