// Package agent defines the public agent runtime contract and its typed event
// stream. Consumers such as the TUI, RPC servers, or embedding applications
// depend only on this package and never on an execution environment.
package agent

import (
	"context"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

const (
	// DefaultMaxTurns is the default LLM turn budget per run.
	DefaultMaxTurns = 100

	// DefaultMaxToolCalls is the default tool execution budget per run.
	DefaultMaxToolCalls = 200

	// DefaultToolTimeout is the default timeout for one tool execution.
	DefaultToolTimeout = 5 * time.Minute
)

// Agent is a stateful conversational agent. A single run is active at a time;
// events are delivered to subscribers synchronously from the run goroutine, so
// handlers must be fast and must not call Run or Close.
type Agent interface {
	// Run executes one turn of the agentic loop. It returns when the run
	// finishes, is cancelled, or fails. Cancellation is propagated to the
	// provider, approvals, and tools. Implementations must emit AgentFinished
	// for every run attempt.
	Run(ctx context.Context, input string) error

	// Subscribe registers an event handler. Subscribe before calling Run;
	// there is no unsubscribe and the subscription lives for the lifetime of
	// the agent.
	Subscribe(handler func(Event))

	// History returns a snapshot of the current conversation history,
	// including any system message.
	History() []provider.Message

	// LoadHistory replaces the conversation history while the agent is idle.
	// If the messages do not start with a system message, the agent prepends
	// its system prompt. Loading an empty history clears the conversation.
	LoadHistory(messages []provider.Message) error

	// Cancel cancels the current run, if any.
	Cancel()

	// Close cancels the active run, waits for cleanup, and releases resources.
	Close() error

	// SetApprover installs the approval mechanism used for risky tool calls.
	SetApprover(approver Approver)

	// Approver returns the currently installed approver.
	Approver() Approver

	// ApprovalTimeout reports how long the runtime waits for an approval
	// decision. Consumers that display their own prompt should mirror it.
	ApprovalTimeout() time.Duration
}

// ApprovalRequest describes an action that needs approval.
type ApprovalRequest struct {
	Kind     string   `json:"kind"`
	Command  string   `json:"command"`
	Workdir  string   `json:"workdir"`
	Programs []string `json:"programs,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// ApprovalDecision is the answer to an ApprovalRequest.
type ApprovalDecision struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// Approver decides whether a risky action may run. Implementations must
// respect the supplied context.
type Approver interface {
	Approve(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error)
}

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	return f(ctx, request)
}

// EventType identifies an event in the agent stream.
type EventType string

const (
	EventAgentStarted  EventType = "agent.started"
	EventAgentFinished EventType = "agent.finished"
	EventTurnStarted   EventType = "agent.turn.started"

	EventAssistantDelta EventType = "assistant.delta"
	EventReasoningDelta EventType = "reasoning.delta"

	EventToolStarted   EventType = "tool.started"
	EventToolProgress  EventType = "tool.progress"
	EventToolCompleted EventType = "tool.completed"
	EventToolFailed    EventType = "tool.failed"

	EventUsage                     EventType = "usage"
	EventContextCompactionStarted  EventType = "context.compaction.started"
	EventContextCompactionFinished EventType = "context.compaction.finished"
	EventAgentError                EventType = "agent.error"

	// Deprecated: use EventContextCompactionFinished.
	EventContextCompacted = EventContextCompactionFinished
)

// Event is implemented by every typed agent event.
type Event interface {
	Type() EventType
}

var (
	_ Event = AgentStarted{}
	_ Event = AgentFinished{}
	_ Event = TurnStarted{}
	_ Event = AssistantDelta{}
	_ Event = ReasoningDelta{}
	_ Event = ToolStarted{}
	_ Event = ToolProgress{}
	_ Event = ToolCompleted{}
	_ Event = ToolFailed{}
	_ Event = Usage{}
	_ Event = ContextCompactionStarted{}
	_ Event = ContextCompactionFinished{}
	_ Event = AgentError{}
)

// AgentStarted is emitted once at the beginning of a run.
type AgentStarted struct {
	At    time.Time `json:"at"`
	Input string    `json:"input"`
}

func (AgentStarted) Type() EventType { return EventAgentStarted }

// AgentFinished is emitted once when a run ends.
type AgentFinished struct {
	At time.Time `json:"at"`
	// Err is empty for a successful run.
	Err string `json:"error,omitempty"`
	// Canceled reports that the run ended because its context was cancelled.
	Canceled bool `json:"canceled,omitempty"`
}

func (AgentFinished) Type() EventType { return EventAgentFinished }

// TurnStarted marks the beginning of one LLM turn. Consumers can use it to
// flush buffered deltas from the previous turn.
type TurnStarted struct {
	At   time.Time `json:"at"`
	Turn int       `json:"turn"`
}

func (TurnStarted) Type() EventType { return EventTurnStarted }

// AssistantDelta carries an assistant text increment.
type AssistantDelta struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

func (AssistantDelta) Type() EventType { return EventAssistantDelta }

// ReasoningDelta carries a reasoning text increment.
type ReasoningDelta struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

func (ReasoningDelta) Type() EventType { return EventReasoningDelta }

// ToolStarted is emitted immediately before a tool executes.
type ToolStarted struct {
	At         time.Time      `json:"at"`
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args,omitempty"`
}

func (ToolStarted) Type() EventType { return EventToolStarted }

// ToolProgress carries streaming tool arguments or an execution progress
// message.
type ToolProgress struct {
	At             time.Time `json:"at"`
	ToolCallID     string    `json:"tool_call_id,omitempty"`
	ToolName       string    `json:"tool_name,omitempty"`
	ArgumentsDelta string    `json:"arguments_delta,omitempty"`
	Message        string    `json:"message,omitempty"`
}

func (ToolProgress) Type() EventType { return EventToolProgress }

// ToolCompleted is emitted after a successful tool execution.
type ToolCompleted struct {
	At         time.Time      `json:"at"`
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Result     map[string]any `json:"result,omitempty"`
	Duration   time.Duration  `json:"duration"`
}

func (ToolCompleted) Type() EventType { return EventToolCompleted }

// ToolFailed is emitted when a tool execution fails, times out, or is skipped.
type ToolFailed struct {
	At         time.Time     `json:"at"`
	ToolCallID string        `json:"tool_call_id"`
	ToolName   string        `json:"tool_name"`
	Message    string        `json:"message"`
	Duration   time.Duration `json:"duration"`
}

func (ToolFailed) Type() EventType { return EventToolFailed }

// Usage reports token accounting for an LLM call.
type Usage struct {
	At               time.Time `json:"at"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
}

func (Usage) Type() EventType { return EventUsage }

// ContextCompactionStarted is emitted before Iris begins summarizing or
// trimming history. Consumers can show a compaction-in-progress status.
type ContextCompactionStarted struct {
	At           time.Time `json:"at"`
	Window       int       `json:"window"`
	BeforeTokens int       `json:"before_tokens"`
	Forced       bool      `json:"forced,omitempty"`
}

func (ContextCompactionStarted) Type() EventType { return EventContextCompactionStarted }

// ContextCompactionFinished reports that a compaction pass ended. The counters
// are zero when a pass was started but made no changes (for example because it
// failed before summarizing).
type ContextCompactionFinished struct {
	At                      time.Time `json:"at"`
	Window                  int       `json:"window"`
	BeforeTokens            int       `json:"before_tokens"`
	AfterTokens             int       `json:"after_tokens"`
	SummarizedMessages      int       `json:"summarized_messages"`
	TrimmedToolResults      int       `json:"trimmed_tool_results"`
	SummaryPromptTokens     int       `json:"summary_prompt_tokens"`
	SummaryCompletionTokens int       `json:"summary_completion_tokens"`
	SummaryFallback         bool      `json:"summary_fallback,omitempty"`
	Forced                  bool      `json:"forced,omitempty"`
}

func (ContextCompactionFinished) Type() EventType { return EventContextCompactionFinished }

// Deprecated: use ContextCompactionFinished.
type ContextCompacted = ContextCompactionFinished

// AgentError is emitted for a run-level error. Tool failures are reported with
// ToolFailed instead.
type AgentError struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (AgentError) Type() EventType { return EventAgentError }
