// Package agent defines the public agent runtime contract and its typed event
// stream. Consumers such as the TUI, RPC servers, or embedding applications
// depend only on this package and never on an execution environment.
package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

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
// handlers must be fast and must not call Run, Close, or LoadHistory.
//
// While a run is active, callers can control it without stopping the process:
// Cancel and Abort stop the run, Steer queues instructions for the next turn,
// and FollowUp queues requests for when the run would otherwise stop. Queue
// methods may be called from event handlers.
type Agent interface {
	// Run executes one agent run. It returns when the run is settled: all
	// requested tools, retries, compaction, steering, and follow-ups are
	// finished, or the run was cancelled or failed. Cancellation is propagated
	// to the provider, approvals, and tools. Implementations must emit
	// AgentFinished for every run attempt.
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

	// Cancel aborts the current run, if any. It is the "abort" operation:
	// the run stops promptly, while conversation history remains valid for a
	// later run. Queued instructions that have not been delivered are
	// discarded when the run ends; use Abort or ClearQueue first to recover
	// their text.
	Cancel()

	// Abort stops the current run and atomically returns instructions that
	// were queued but not delivered. After Abort, no further instruction is
	// accepted for that run. It is the recommended way to implement an abort
	// UI that restores queued text to the editor.
	Abort() Queue

	// Steer queues an instruction for the active run. It is delivered as a
	// user message at the next turn boundary: after the current assistant turn
	// and all of its tool calls finish, before the next LLM request. If the run
	// would otherwise stop, a steering instruction gives it one more turn.
	//
	// Steer never interrupts an in-flight provider request or tool execution;
	// use Cancel for that. It returns ErrAgentIdle when no run is active,
	// ErrAgentClosed after Close, and ErrEmptyInput for blank input.
	Steer(input string) error

	// FollowUp queues a request for after the active run would otherwise stop.
	// Follow-ups are delivered only once no tool calls remain and no steering
	// instructions are pending. A delivered follow-up continues the same run,
	// preserving conversation state and budgets. It returns the same errors as
	// Steer.
	FollowUp(input string) error

	// ClearQueue removes and returns all queued steering instructions and
	// follow-ups. It is safe to call while a run is active, from an event
	// handler, or while idle.
	ClearQueue() Queue

	// Pending returns a snapshot of queued instructions without clearing them.
	Pending() Queue

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

// AssistantRecord is one assistant message to persist durably. ToolCalls must
// be persisted before any of them execute.
type AssistantRecord struct {
	Content   string
	Reasoning string
	ToolCalls []provider.ToolCall
	Usage     provider.Usage
	Model     string
}

// ToolResultRecord is one tool result to persist immediately after the tool
// returns.
type ToolResultRecord struct {
	ToolCallID string
	ToolName   string
	Content    string
}

// Recorder persists transcript entries at durability points. Implementations
// must write and sync before returning; the runtime calls these methods
// synchronously on the run goroutine and aborts the run if they fail.
type Recorder interface {
	// RecordUser persists a user message before the model call that follows it.
	RecordUser(text string) error

	// RecordAssistant persists an assistant message before any of its tool
	// calls execute.
	RecordAssistant(record AssistantRecord) error

	// RecordToolResult persists a tool result immediately after execution.
	RecordToolResult(record ToolResultRecord) error
}

// RecorderAgent is implemented by agents that support durable transcript
// recording. Embedders can type-assert an Agent to install a Recorder.
type RecorderAgent interface {
	Agent
	SetRecorder(recorder Recorder)
}

// ToolOutputAgent is implemented by agents that can persist full tool output
// in the session store's tool-output directory. Embedders and the client/TUI
// use it to keep the complete output readable after the model sees a truncated
// result.
type ToolOutputAgent interface {
	Agent
	SetToolOutputDir(dir string) error
}

// Queue is a snapshot of instructions waiting for delivery.
type Queue struct {
	// Steering holds instructions delivered at the next turn boundary.
	Steering []string `json:"steering,omitempty"`

	// FollowUps holds requests delivered when the run would otherwise stop.
	FollowUps []string `json:"follow_ups,omitempty"`
}

// InstructionKind identifies where a queued instruction is delivered.
type InstructionKind string

const (
	// InstructionSteering is delivered at the next turn boundary.
	InstructionSteering InstructionKind = "steering"

	// InstructionFollowUp is delivered when the run would otherwise stop.
	InstructionFollowUp InstructionKind = "follow_up"
)

// EventType identifies an event in the agent stream.
type EventType string

const (
	EventAgentStarted  EventType = "agent.started"
	EventAgentFinished EventType = "agent.finished"
	EventTurnStarted   EventType = "agent.turn.started"
	EventTurnFinished  EventType = "agent.turn.finished"
	EventTurnRetried   EventType = "agent.turn.retried"

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

	// EventQueueUpdated reports the instructions still waiting after the
	// runtime delivered or discarded messages.
	EventQueueUpdated EventType = "agent.queue.updated"

	// EventInstructionDelivered reports that a queued instruction became a
	// user message in the conversation.
	EventInstructionDelivered EventType = "agent.instruction.delivered"

	// Deprecated: use EventContextCompactionFinished.
	EventContextCompacted = EventContextCompactionFinished

	// EventAgentSettled is the settled-event name for EventAgentFinished. The
	// wire value remains "agent.finished".
	EventAgentSettled = EventAgentFinished
)

// Event is implemented by every typed agent event.
type Event interface {
	Type() EventType
}

var (
	_ Event = AgentStarted{}
	_ Event = AgentFinished{}
	_ Event = TurnStarted{}
	_ Event = TurnFinished{}
	_ Event = TurnRetried{}
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
	_ Event = QueueUpdated{}
	_ Event = InstructionDelivered{}
)

// AgentStarted is emitted once at the beginning of a run.
type AgentStarted struct {
	At    time.Time `json:"at"`
	Input string    `json:"input"`
}

func (AgentStarted) Type() EventType { return EventAgentStarted }

// AgentFinished is the settled terminal event: it is emitted once when no more
// automatic work remains for the run. It may be preceded by retries,
// compaction, tool execution, steering, and follow-ups. The completion of an
// individual model request is reported by TurnFinished instead.
//
// AgentSettled is an alias that names this semantic explicitly. The wire event
// remains "agent.finished".
type AgentFinished struct {
	At time.Time `json:"at"`
	// Err is empty for a successful run.
	Err string `json:"error,omitempty"`
	// Canceled reports that the run ended because its context was cancelled.
	Canceled bool `json:"canceled,omitempty"`
	// Summary is collected from runtime observations, not model output. It is
	// nil when the run failed before it began.
	Summary *RunSummary `json:"summary,omitempty"`
}

func (AgentFinished) Type() EventType { return EventAgentFinished }

// AgentSettled names AgentFinished's settled semantics. The wire event remains
// "agent.finished".
type AgentSettled = AgentFinished

// FileChange is one working-tree entry observed with git status.
type FileChange struct {
	// Status is a compact git status: M, A, D, R, C, T, or U.
	Status string `json:"status"`
	Path   string `json:"path"`
}

// RunSummary is execution metadata collected from the runtime. It is not a
// model-generated summary: counters come from actual requests, tool calls, and
// usage; files come from the working tree at the end of the run.
type RunSummary struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`

	// LLMRequests counts provider calls, including compaction summaries and
	// context-overflow retries.
	LLMRequests int `json:"llm_requests"`

	// ToolExecutions counts tools that actually started. Invalid, skipped, or
	// budget-exhausted tool calls are not executions.
	ToolExecutions int `json:"tool_executions"`

	// Usage is the total reported by all provider responses in the run,
	// including compaction summaries and retries.
	Usage provider.Usage `json:"usage"`

	// GitRepository reports whether the workspace was inside a Git work tree
	// when the summary was collected.
	GitRepository bool `json:"git_repository,omitempty"`

	// FilesChanged is the working-tree status at the end of the run. It can
	// include changes that predated the run.
	FilesChanged []FileChange `json:"files_changed,omitempty"`
}

// Duration returns the run duration from the observed timestamps.
func (s *RunSummary) Duration() time.Duration {
	if s == nil {
		return 0
	}
	if s.DurationMS > 0 {
		return time.Duration(s.DurationMS) * time.Millisecond
	}
	return s.FinishedAt.Sub(s.StartedAt)
}

// Format renders a concise human-readable completion summary. outcome is
// usually "Task completed", "Run canceled", or "Run failed".
func (s *RunSummary) Format(outcome string) string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(outcome) == "" {
		outcome = "Task completed"
	}

	var builder strings.Builder
	builder.WriteString(outcome)
	builder.WriteString("\n")

	if s.GitRepository {
		builder.WriteString("\nFiles changed:\n")
		if len(s.FilesChanged) == 0 {
			builder.WriteString("  none\n")
		} else {
			const maxFiles = 100
			shown := len(s.FilesChanged)
			if shown > maxFiles {
				shown = maxFiles
			}
			for _, change := range s.FilesChanged[:shown] {
				fmt.Fprintf(
					&builder,
					"  %s %s\n",
					sanitizeSummaryText(change.Status),
					sanitizeSummaryText(change.Path),
				)
			}
			if len(s.FilesChanged) > shown {
				fmt.Fprintf(&builder, "  ... %d more\n", len(s.FilesChanged)-shown)
			}
		}
	}

	totalTokens := s.Usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = s.Usage.PromptTokens + s.Usage.CompletionTokens
	}

	builder.WriteString("\n")
	fmt.Fprintf(&builder, "Tool executions: %d\n", s.ToolExecutions)
	fmt.Fprintf(&builder, "LLM requests: %d\n", s.LLMRequests)
	fmt.Fprintf(&builder, "Tokens consumed: %s\n", formatCount(totalTokens))
	fmt.Fprintf(&builder, "Duration: %s\n", formatDuration(s.Duration()))
	return builder.String()
}

func formatCount(value int) string {
	text := fmt.Sprintf("%d", value)
	if len(text) <= 3 {
		return text
	}
	var builder strings.Builder
	offset := len(text) % 3
	if offset > 0 {
		builder.WriteString(text[:offset])
	}
	for index := offset; index < len(text); index += 3 {
		if builder.Len() > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(text[index : index+3])
	}
	return builder.String()
}

func sanitizeSummaryText(text string) string {
	var builder strings.Builder
	for _, char := range text {
		switch {
		case char == '\n' || char == '\r' || char == '\t':
			builder.WriteByte(' ')
		case unicode.IsControl(char):
			builder.WriteRune('?')
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func formatDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0ms"
	}
	if duration < time.Second {
		millis := duration.Milliseconds()
		if millis < 1 {
			millis = 1
		}
		return fmt.Sprintf("%dms", millis)
	}
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		minutes := int(duration / time.Minute)
		seconds := int((duration % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := int(duration / time.Hour)
	minutes := int((duration % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

// TurnStarted marks the beginning of one model request. Consumers can use it
// to flush buffered deltas from the previous turn.
type TurnStarted struct {
	At   time.Time `json:"at"`
	Turn int       `json:"turn"`
}

func (TurnStarted) Type() EventType { return EventTurnStarted }

// TurnFinished marks the completion of one model request. The run may still
// execute tools, deliver steering or follow-ups, compact context, or retry;
// AgentFinished is the settled terminal event. Consumers must not treat
// TurnFinished as the end of the execution.
type TurnFinished struct {
	At   time.Time `json:"at"`
	Turn int       `json:"turn"`

	// Attempts is the number of model requests made for this turn, including
	// context-overflow recovery attempts.
	Attempts int `json:"attempts"`

	// FinishReason is the provider finish reason ("stop", "tool_calls",
	// "length", ...). It is empty when the request failed before a response.
	FinishReason string `json:"finish_reason,omitempty"`

	// ToolCalls is the number of tool calls returned by the model.
	ToolCalls int `json:"tool_calls,omitempty"`

	// ContentBytes and ReasoningBytes are the sizes of the final assistant
	// content for the turn.
	ContentBytes   int `json:"content_bytes,omitempty"`
	ReasoningBytes int `json:"reasoning_bytes,omitempty"`

	// Err is empty when the request completed successfully.
	Err string `json:"error,omitempty"`

	// Canceled reports that the request ended because the run was cancelled.
	Canceled bool `json:"canceled,omitempty"`
}

func (TurnFinished) Type() EventType { return EventTurnFinished }

// TurnRetried reports that a model request failed in a recoverable way and the
// runtime is retrying it. Consumers must discard partial assistant/reasoning
// output and tool previews accumulated for this turn before processing the
// retry's deltas.
type TurnRetried struct {
	At   time.Time `json:"at"`
	Turn int       `json:"turn"`

	// Reason is a stable code such as "context_overflow".
	Reason string `json:"reason"`

	// Error describes the failed attempt.
	Error string `json:"error,omitempty"`

	// NextAttempt is the attempt number of the retry.
	NextAttempt int `json:"next_attempt"`
}

func (TurnRetried) Type() EventType { return EventTurnRetried }

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

// ToolFailed is emitted when a tool execution fails, times out, is rejected
// before execution, or is skipped.
type ToolFailed struct {
	At         time.Time     `json:"at"`
	ToolCallID string        `json:"tool_call_id"`
	ToolName   string        `json:"tool_name"`
	Message    string        `json:"message"`
	Duration   time.Duration `json:"duration"`
	// Code is a stable machine-readable error code. See the execution-control
	// documentation for the values emitted by the built-in runtime.
	Code string `json:"code,omitempty"`
	// Executed reports whether the tool was actually invoked. It is false for
	// invalid or truncated arguments, unknown tools, and skipped calls.
	Executed bool `json:"executed,omitempty"`
	// Retryable reports whether repeating the call is safe. It is true for
	// validation failures that happened before execution and false once a tool
	// ran, because repeating a tool can repeat side effects.
	Retryable bool `json:"retryable,omitempty"`
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

// QueueUpdated reports the instructions that are still waiting to be delivered
// after the runtime delivered some (or discarded them when the run ended). It
// is emitted from the run goroutine; enqueueing itself is acknowledged by the
// return value of Steer and FollowUp.
type QueueUpdated struct {
	At        time.Time `json:"at"`
	Steering  []string  `json:"steering,omitempty"`
	FollowUps []string  `json:"follow_ups,omitempty"`
}

func (QueueUpdated) Type() EventType { return EventQueueUpdated }

// InstructionDelivered reports that a queued instruction was appended to the
// conversation as a user message and will be visible to the model on the
// current turn.
type InstructionDelivered struct {
	At   time.Time       `json:"at"`
	Kind InstructionKind `json:"kind"`
	Text string          `json:"text"`
}

func (InstructionDelivered) Type() EventType { return EventInstructionDelivered }
