package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/prompt"
	"github.com/m4ns0ur/iris/internal/tool"
)

var Logger = logger.Logger

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
	a.history = append(a.history, llm.ChatMessage{Role: "system", Content: prompt.BuildSystemPrompt()})
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

	resp, err := a.client.Generate(a.history, nil, onChunk, onError)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	decision := extractJSON(resp)

	Logger.Debug("Decision received", "decision", decision)

	if decision != nil {
		if state, ok := (*decision)["state"].(string); ok {
			Logger.Debug("State updated based on decision", "newState", a.state)
			a.state = state
		}
		if response, ok := (*decision)["response"].(string); ok {
			a.history = append(a.history, llm.ChatMessage{Role: "assistant", Content: response})
		}
		if toolName, ok := (*decision)["tool_name"].(string); ok {
			a.toolName = toolName
			if toolArgs, ok := (*decision)["tool_args"].(map[string]any); ok {
				a.toolArgs = toolArgs
			}
			Logger.Debug("Tool execution decision", "toolName", a.toolName, "toolArgs", a.toolArgs)
		}
	}

	return nil
}

func extractJSON(text string) *map[string]any {
	text = strings.TrimSpace(text)

	start := strings.Index(text, "{")
	if start == -1 {
		return nil
	}

	depth := 0
	end := -1
	for i := start; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}

	if end == -1 {
		return nil
	}

	jsonStr := text[start : end+1]
	var result map[string]any
	err := json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		return nil
	}

	return &result
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
