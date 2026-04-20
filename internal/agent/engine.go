package agent

import (
	"context"
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
	history     []Message
	maxSteps    int
	workdir     string
	toolName    string
	toolArgs    map[string]any
}

type Message = prompt.Message

func NewAgent(client *llm.LLMClient, workdir string) *Agent {
	return &Agent{
		client:      client,
		state:       "idle",
		goals:       []string{},
		currentTask: -1,
		history:     []Message{},
		maxSteps:    50, // Prevent infinite loops
		workdir:     workdir,
	}
}

// Run executes the agentic loop with streaming support
func (a *Agent) Run(input string, onChunk func(chunkType string, chunk string), onError func(error)) error {
	a.reset()
	// Initialize with system prompt and user input
	a.history = append(a.history, Message{Role: "user", Content: input})
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
	a.history = []Message{}
}

func (a *Agent) loop(onChunk func(chunkType string, chunk string), onError func(error)) error {
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
			if err != nil {
				return fmt.Errorf("tool execution failed: %w", err)
			}

			Logger.Debug("Tool result", "toolName", a.toolName, "result", result)

			// Add result to conversation history
			a.history = append(a.history, Message{
				Role:    "assistant",
				Content: fmt.Sprintf("Executed tool '%s': %s", a.toolName, formatResult(result)),
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
			return nil

		default:
			Logger.Warn("Unknown state", "state", a.state)
			a.state = "idle"
		}

		if a.shouldStop() {
			Logger.Debug("Stopping agent loop")
			break
		}

		a.step++
	}

	return nil
}

func (a *Agent) shouldStop() bool {
	if a.state == "done" {
		Logger.Debug("Agent state is done")
		return true
	}

	if a.step >= a.maxSteps {
		Logger.Warn("Max steps reached")
		return true
	}

	if a.state == "executing" && !a.isTaskComplete() {
		Logger.Debug("Task not complete, start reflecting")
		a.state = "reflecting"
	}

	return false
}

func (a *Agent) buildHistoryContext() string {
	if len(a.history) < 1 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\nConversation History:\n")

	// Show last 5 messages or all if fewer than 5
	start := max(len(a.history)-5, 0)

	for i := start; i < len(a.history); i++ {
		fmt.Fprintf(&sb, "\n%s: %s", a.history[i].Role, truncateString(a.history[i].Content, 200))
	}

	return sb.String()
}
