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
	client           *llm.LLMClient
	state            string
	step             int
	goals            []string
	currentTask      int
	history          []llm.ChatMessage
	maxSteps         int
	workdir          string
	pendingToolCalls []PendingToolCall
}

type PendingToolCall struct {
	ID   string
	Name string
	Args map[string]any
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
	a.history = append(a.history, llm.ChatMessage{Role: "user", Content: []llm.Content{{Type: "text", Text: input}}})
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

		if err := a.next(onChunk, onError); err != nil {
			return err
		}

		switch a.state {
		case "respond":
			Logger.Debug("Agent is responding to user")
			return nil

		case "tool":
			ctx := context.Background()
			ctx = tool.WithWorkdir(ctx, a.workdir)

			for _, call := range a.pendingToolCalls {
				result, err := tool.ExecuteWithTools(
					ctx,
					registry,
					call.Name,
					call.Args,
				)

				var resultStr string
				if err != nil {
					resultStr = fmt.Sprintf("failed: %v", err)
				} else {
					resultStr = formatResult(result)
				}

				Logger.Debug(
					"Tool result",
					"toolName", call.Name,
					"toolCallID", call.ID,
					"result", resultStr,
				)

				a.history = append(a.history, llm.ChatMessage{
					Role:       "tool",
					Content:    resultStr,
					ToolCallID: call.ID,
				})
			}

			a.pendingToolCalls = nil
			a.state = "idle"

		case "done":
			Logger.Debug("Agent loop completed")
			return nil

		default:
			Logger.Warn("Unknown state", "state", a.state)
			a.state = "idle"
		}

		a.step++
	}

	Logger.Warn("Max steps reached", "maxSteps", a.maxSteps)
	return nil
}

func (a *Agent) next(onChunk func(chunkType string, content string), onError func(error)) error {
	Logger.Debug("Decision phase", "history", a.history)

	tools := toolContext.Registry.List()

	chunk, err := a.client.Generate(
		a.history,
		tools,
		nil,
		onChunk,
		onError,
	)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	Logger.Debug("next: LLM response received", "response", chunk)

	delta := chunk.Choices[0].Delta

	msg := llm.ChatMessage{
		Role:    "assistant",
		Content: delta.Content,
	}

	if len(delta.ToolCalls) > 0 {
		a.pendingToolCalls = nil

		msg.ToolCalls = make([]llm.ToolCall, 0, len(delta.ToolCalls))

		for _, tc := range delta.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				Id:   tc.Id,
				Type: tc.Type,
				Function: llm.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})

			call := PendingToolCall{
				ID:   tc.Id,
				Name: tc.Function.Name,
			}

			if tc.Function.Arguments != "" {
				var args map[string]any

				if err := json.Unmarshal(
					[]byte(tc.Function.Arguments),
					&args,
				); err != nil {
					Logger.Error(
						"Failed to parse tool arguments",
						"tool", tc.Function.Name,
						"error", err,
					)
					args = map[string]any{}
				}

				call.Args = args
			}

			a.pendingToolCalls = append(a.pendingToolCalls, call)
		}

		a.history = append(a.history, msg)

		Logger.Debug(
			"Tool execution decision",
			"count", len(a.pendingToolCalls),
		)

		a.state = "tool"
		return nil
	}

	if delta.Content != "" {
		a.history = append(a.history, msg)
	}

	if chunk.Choices[0].FinishReason == "stop" {
		a.state = "done"
		return nil
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
