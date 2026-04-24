package agent

import (
	"context"
	"fmt"

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
		err := a.decide(onChunk, onError)
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

		// case "idle":
		// 	if err := a.plan(onChunk, onError); err != nil {
		// 		return err
		// 	}

		// case "planning":
		// 	if err := a.executePlan(onChunk, onError); err != nil {
		// 		return err
		// 	}

		// case "executing":
		// 	if err := a.runTask(onChunk, onError); err != nil {
		// 		return err
		// 	}

		// case "reflecting":
		// 	if err := a.reflect(onChunk, onError); err != nil {
		// 		return err
		// 	}

		case "done":
			Logger.Debug("Agent loop completed")
			break

		default:
			Logger.Warn("Unknown state", "state", a.state)
			a.state = "idle"
		}

		if a.step >= a.maxSteps {
			Logger.Warn("Max steps reached")
			break
		}

		if a.state == "executing" && !a.isTaskComplete() {
			Logger.Debug("Task not complete, start reflecting")
			a.state = "reflecting"
		}

		a.step++
	}

	return nil
}
