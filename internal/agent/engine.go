package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/logger"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/internal/prompt"
	"github.com/m4ns0ur/iris/internal/tool"
)

var Logger = logger.Logger

const (
	// DefaultMaxTurns bounds the number of LLM turns in one run.
	DefaultMaxTurns = 100

	// DefaultMaxToolCalls bounds the number of tool executions in one run.
	DefaultMaxToolCalls = 200

	// DefaultToolTimeout bounds a single tool execution.
	DefaultToolTimeout = 5 * time.Minute
)

// Options configures execution budgets for an Agent.
type Options struct {
	// MaxTurns bounds the number of LLM turns in one run. Zero uses
	// DefaultMaxTurns.
	MaxTurns int

	// MaxToolCalls bounds the number of tool executions in one run. Zero uses
	// DefaultMaxToolCalls.
	MaxToolCalls int

	// ToolTimeout bounds a single tool execution. Zero uses
	// DefaultToolTimeout.
	ToolTimeout time.Duration
}

type Agent struct {
	// mu guards the run lifecycle. Agent state and history are only mutated by
	// the single active run.
	mu        sync.Mutex
	running   bool
	closed    bool
	runCancel context.CancelFunc
	runWG     sync.WaitGroup

	client           *llm.LLMClient
	state            string
	step             int
	goals            []string
	history          []llm.ChatMessage
	maxTurns         int
	maxToolCalls     int
	toolTimeout      time.Duration
	toolCalls        int
	workdir          string
	pendingToolCalls []PendingToolCall
	policy           *policy.Policy
	registry         *tool.ToolRegistry
	toolContext      *tool.ToolContext
}

type PendingToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

func NewAgent(client *llm.LLMClient, pol *policy.Policy, options ...Options) (*Agent, error) {
	if client == nil {
		return nil, fmt.Errorf("agent requires an LLM client")
	}

	registry, err := tool.NewDefaultRegistry(pol)
	if err != nil {
		return nil, err
	}

	opts := Options{}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultMaxTurns
	}
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = DefaultMaxToolCalls
	}
	if opts.ToolTimeout <= 0 {
		opts.ToolTimeout = DefaultToolTimeout
	}

	workdir := pol.Workdir()
	toolContext := &tool.ToolContext{
		Registry: registry,
		Workdir:  workdir,
		Policy:   pol,
	}

	return &Agent{
		client:       client,
		state:        "idle",
		goals:        []string{},
		history:      []llm.ChatMessage{},
		maxTurns:     opts.MaxTurns,
		maxToolCalls: opts.MaxToolCalls,
		toolTimeout:  opts.ToolTimeout,
		workdir:      workdir,
		policy:       pol,
		registry:     registry,
		toolContext:  toolContext,
	}, nil
}

// Cancel stops the current run, if any. It is safe to call concurrently and
// when no run is active.
func (a *Agent) Cancel() {
	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Close cancels any active run, waits for it to release tool resources, and
// removes the session's temporary files. It must not be called from inside an
// agent callback.
func (a *Agent) Close() error {
	a.mu.Lock()
	a.closed = true
	cancel := a.runCancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Wait for the run to observe cancellation and finish its deferred cleanup
	// before deleting the temp directory it may still be using.
	a.runWG.Wait()

	if a.registry == nil {
		return nil
	}
	return a.registry.Close()
}

func (a *Agent) beginRun(parent context.Context) (context.Context, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return nil, ErrAgentClosed
	}
	if a.running {
		return nil, ErrAgentBusy
	}

	a.running = true
	a.step = 0
	a.toolCalls = 0
	a.state = "idle"
	a.pendingToolCalls = nil

	runCtx, cancel := context.WithCancel(parent)
	a.runCancel = cancel
	a.runWG.Add(1)

	return runCtx, nil
}

func (a *Agent) finishRun() {
	a.mu.Lock()
	cancel := a.runCancel
	a.runCancel = nil
	a.running = false
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	a.runWG.Done()
}

// Run executes the agentic loop using a background context. Prefer
// RunContext when the caller needs cancellation.
func (a *Agent) Run(input string, onChunk func(chunkType string, content string), onError func(error)) error {
	return a.RunContext(context.Background(), input, onChunk, onError)
}

// RunFresh executes a run with a fresh conversation history.
func (a *Agent) RunFresh(input string, onChunk func(chunkType string, content string), onError func(error)) error {
	return a.RunFreshContext(context.Background(), input, onChunk, onError)
}

// RunContext executes the agentic loop until completion, cancellation, a
// budget is exhausted, or an error occurs.
func (a *Agent) RunContext(
	ctx context.Context,
	input string,
	onChunk func(chunkType string, content string),
	onError func(error),
) error {
	runCtx, err := a.beginRun(ctx)
	if err != nil {
		return err
	}
	defer a.finishRun()

	onChunk, onError = defaultCallbacks(onChunk, onError)

	a.history = append(a.history, llm.ChatMessage{
		Role:    "user",
		Content: []llm.Content{{Type: "text", Text: input}},
	})

	return a.loop(runCtx, onChunk, onError)
}

// RunFreshContext resets the conversation and executes a run.
func (a *Agent) RunFreshContext(
	ctx context.Context,
	input string,
	onChunk func(chunkType string, content string),
	onError func(error),
) error {
	runCtx, err := a.beginRun(ctx)
	if err != nil {
		return err
	}
	defer a.finishRun()

	onChunk, onError = defaultCallbacks(onChunk, onError)

	a.reset()
	a.history = append(a.history, llm.ChatMessage{
		Role:    "system",
		Content: prompt.BuildSystemPrompt(a.toolContext),
	})
	a.history = append(a.history, llm.ChatMessage{
		Role:    "user",
		Content: []llm.Content{{Type: "text", Text: input}},
	})

	return a.loop(runCtx, onChunk, onError)
}

func defaultCallbacks(
	onChunk func(chunkType string, content string),
	onError func(error),
) (func(string, string), func(error)) {
	if onChunk == nil {
		onChunk = func(string, string) {}
	}
	if onError == nil {
		onError = func(error) {}
	}
	return onChunk, onError
}

func (a *Agent) reset() {
	Logger.Info("Resetting agent state")

	a.state = "idle"
	a.step = 0
	a.goals = []string{}
	a.history = []llm.ChatMessage{}
	a.pendingToolCalls = nil
}

func (a *Agent) loop(
	ctx context.Context,
	onChunk func(chunkType string, content string),
	onError func(error),
) error {
	Logger.Info("Starting agentic loop")

	for {
		if err := ctx.Err(); err != nil {
			Logger.Info("Agent run canceled", "error", err)
			return err
		}

		if a.step >= a.maxTurns {
			return fmt.Errorf("%w: max_turns=%d", ErrTurnBudgetExceeded, a.maxTurns)
		}

		Logger.Debug(
			"Agentic Loop",
			"step", a.step,
			"state", a.state,
		)

		onChunk("next", fmt.Sprintf("%d", a.step))

		if err := a.next(ctx, onChunk, onError); err != nil {
			return err
		}

		switch a.state {
		case "tool":
			if err := a.executeTools(ctx); err != nil {
				return err
			}

		case "done":
			Logger.Info("Agentic loop completed")
			return nil

		default:
			return fmt.Errorf("unknown agent state %q", a.state)
		}

		a.step++
	}
}

func (a *Agent) executeTools(ctx context.Context) error {
	Logger.Info("Agent is calling tools")
	Logger.Debug("Tool calls", "count", len(a.pendingToolCalls))

	toolCtxBase := tool.WithWorkdir(ctx, a.workdir)
	toolCtxBase = tool.WithPolicy(toolCtxBase, a.policy)

	pending := a.pendingToolCalls

	for i, call := range pending {
		if err := ctx.Err(); err != nil {
			a.appendSkippedToolResults(pending[i:], "run canceled before execution")
			a.pendingToolCalls = nil
			return err
		}

		if a.toolCalls >= a.maxToolCalls {
			a.appendSkippedToolResults(
				pending[i:],
				fmt.Sprintf("tool execution budget exhausted (max_tool_calls=%d)", a.maxToolCalls),
			)
			a.pendingToolCalls = nil
			return fmt.Errorf("%w: max_tool_calls=%d", ErrToolBudgetExceeded, a.maxToolCalls)
		}

		a.toolCalls++

		toolCtx := toolCtxBase
		var cancel context.CancelFunc
		if a.toolTimeout > 0 {
			toolCtx, cancel = context.WithTimeout(toolCtxBase, a.toolTimeout)
		}

		result, err := tool.ExecuteWithTools(toolCtx, a.registry, call.Name, call.Args)

		timedOut := cancel != nil && errors.Is(toolCtx.Err(), context.DeadlineExceeded)
		if cancel != nil {
			cancel()
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			a.appendToolResult(call, fmt.Sprintf("failed: run canceled: %v", ctxErr))
			a.appendSkippedToolResults(pending[i+1:], "run canceled before execution")
			a.pendingToolCalls = nil
			return ctxErr
		}

		if timedOut {
			err = fmt.Errorf("%w after %s: %w", ErrToolTimeout, a.toolTimeout, context.DeadlineExceeded)
		}

		var resultStr string
		if err != nil {
			resultStr = fmt.Sprintf("failed: %v", err)
		} else {
			jsonBytes, jsonErr := json.Marshal(result)
			if jsonErr != nil {
				resultStr = fmt.Sprintf("failed: encode tool result: %v", jsonErr)
			} else {
				resultStr = string(jsonBytes)
			}
		}

		a.appendToolResult(call, resultStr)
	}

	a.pendingToolCalls = nil
	a.state = "idle"

	return nil
}

func (a *Agent) appendSkippedToolResults(calls []PendingToolCall, reason string) {
	for _, call := range calls {
		a.appendToolResult(call, "failed: "+reason)
	}
}

func (a *Agent) appendToolResult(call PendingToolCall, content string) {
	Logger.Debug(
		"Tool result",
		"toolName", call.Name,
		"toolCallID", call.ID,
		"result", content,
	)

	a.history = append(a.history, llm.ChatMessage{
		Role:       "tool",
		Content:    content,
		ToolCallID: call.ID,
	})
}

func (a *Agent) next(
	ctx context.Context,
	onChunk func(chunkType string, content string),
	onError func(error),
) error {
	Logger.Info("Generating next response from LLM")
	Logger.Debug("Next response", "historyLength", len(a.history))

	tools := a.toolContext.Registry.List()

	response, err := a.client.Generate(ctx, a.history, tools, onChunk, onError)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("LLM generation failed: %w", err)
	}

	if len(response.Choices) == 0 {
		return fmt.Errorf("LLM returned no choices")
	}

	Logger.Debug("LLM response received", "response", response)

	delta := response.Choices[0].Delta

	msg := llm.ChatMessage{
		Role:             "assistant",
		Content:          delta.Content,
		ReasoningContent: delta.ReasoningContent,
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

				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
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

	if delta.Content != "" {
		Logger.Debug("Message added to history", "message", msg)
		a.history = append(a.history, msg)
	}

	a.state = "done"
	return nil
}
