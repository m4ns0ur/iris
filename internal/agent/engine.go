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
		client:   client,
		state:    "idle",
		goals:    []string{},
		history:  []llm.ChatMessage{},
		maxSteps: 100, // Prevent infinite loops
		workdir:  workdir,
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
	Logger.Info("Resetting agent state")

	a.state = "idle"
	a.step = 0
	a.goals = []string{}
	a.history = []llm.ChatMessage{}
}

func (a *Agent) loop(onChunk func(chunkType string, content string), onError func(error)) error {
	Logger.Info("Starting agentic loop")

	registry := tool.NewDefaultRegistry()

	for a.step < a.maxSteps {
		Logger.Debug(
			"Agentic Loop",
			"step", a.step,
			"state", a.state,
		)

		onChunk("next", fmt.Sprintf("%d", a.step))

		if err := a.next(onChunk, onError); err != nil {
			return err
		}

		switch a.state {
		case "tool":
			Logger.Info("Agent is calling tools")
			Logger.Debug("Tool calls", "count", len(a.pendingToolCalls))

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
					jsonBytes, _ := json.Marshal(result)
					resultStr = string(jsonBytes)
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

				if len(a.pendingToolCalls) > 1 {
					a.step++
					onChunk("next", fmt.Sprintf("%d", a.step))
				}
			}

			a.pendingToolCalls = nil
			a.state = "idle"

		case "done":
			Logger.Info("Agentic loop completed")
			return nil

		default:
			Logger.Error("Unknown state", "state", a.state)
			a.state = "idle"
		}

		a.step++
	}

	Logger.Warn("Max steps reached", "maxSteps", a.maxSteps)
	onChunk("response", "max steps reached")

	return nil
}

func (a *Agent) next(onChunk func(chunkType string, content string), onError func(error)) error {
	Logger.Info("Generating next response from LLM")
	Logger.Debug("Next response", "historyLength", len(a.history))

	tools := toolContext.Registry.List()

	response, err := a.client.Generate(
		a.history,
		tools,
		nil,
		onChunk,
		onError,
	)
	if err != nil {
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	Logger.Debug("LLM response received", "response", response)

	delta := response.Choices[0].Delta

	msg := llm.ChatMessage{
		Role:             "assistant",
		Content:          delta.Content,
		ReasoningContent: delta.ReasoningContent,
	}

	if delta.Content != "" {
		Logger.Debug("Message added to history", "message", msg)
		a.history = append(a.history, msg)
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

		Logger.Debug("Message added to history", "message", msg)
		a.history = append(a.history, msg)
		a.state = "tool"
		return nil
	}

	if response.Choices[0].FinishReason == "stop" {
		a.state = "done"
	}

	return nil
}
