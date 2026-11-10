# Agent API

[← Documentation index](README.md)

---

## Public API

The agent runtime has no dependency on the TUI. Consumers subscribe to a typed event stream and drive runs with a context:

```go
type Agent interface {
    Run(ctx context.Context, input string) error
    Subscribe(handler func(Event))
    Cancel()
    Abort() Queue
    Steer(input string) error
    FollowUp(input string) error
    ClearQueue() Queue
    Pending() Queue
    Close() error
    SetApprover(Approver)
    Approver() Approver
    ApprovalTimeout() time.Duration
    History() []provider.Message
    LoadHistory(messages []provider.Message) error
}
```

`History` returns a snapshot including the system message. `LoadHistory` replaces the conversation while the agent is idle and prepends the system prompt when the messages do not start with one. `Subscribe` has no unsubscribe; the subscription lives for the agent's lifetime. `ApprovalTimeout` reports the policy deadline and `Approver` returns the installed approver, including one configured through an approval hook. `Close` cancels the active run, waits for cleanup, and releases ephemeral tool-output files; session tool-output files persist. A single run is active at a time; starting another run returns `agent.ErrAgentBusy`, and using an agent after `Close` returns `agent.ErrAgentClosed`.

Agents that can durably record a transcript also implement the optional `RecorderAgent` interface:

```go
type Recorder interface {
    RecordUser(text string) error
    RecordAssistant(record AssistantRecord) error
    RecordToolResult(record ToolResultRecord) error
}

type RecorderAgent interface {
    Agent
    SetRecorder(Recorder)
}
```

The built-in agent implements `RecorderAgent`. The client and TUI install a recorder backed by the leased session so `RecordUser` runs before the model call, `RecordAssistant` runs before its tool calls execute, and `RecordToolResult` runs immediately after each tool. Recorder methods are called synchronously on the run goroutine; an error aborts the run with `agent.ErrPersistence`. Custom agents that do not implement `RecorderAgent` still work with the client/TUI using the end-of-run history fallback, but that fallback is less durable across a crash.

Agents may also implement the optional `ToolOutputAgent` interface:

```go
type ToolOutputAgent interface {
    Agent
    SetToolOutputDir(dir string) error
}
```

The built-in agent implements it. `SetToolOutputDir` redirects full tool output to the session directory supplied by `session.ToolOutputProvider` and grants the read tool access to that directory. It must be called while idle; a running agent returns `agent.ErrAgentBusy`. The client and TUI call it automatically when attaching a session. Custom agents that do not implement it keep using the ephemeral tool-output store.

While a run is active, `Steer` and `FollowUp` queue instructions for the next turn and for the stop point respectively. `Pending` snapshots the queues and `ClearQueue` removes and returns them. `Cancel` aborts the run; `Abort` also closes the queue atomically and returns the instructions that had not been delivered, so a UI can restore their text. Queue methods are safe to call from event handlers and from other goroutines, and they never interrupt an in-flight provider request or tool execution. See [Execution Control](execution.md#execution-control) for the exact delivery points.

`agent.turn.finished` is request completion; `agent.finished` is settled execution. Consumers that persist agent history themselves or move to an "idle" state should wait for the settled event. The built-in recorder persists user messages, assistant tool calls, and tool results earlier, at the durability points described in [Sessions](sessions.md#sessions). Consumers that display per-request progress can use `agent.turn.finished`, and should clear partial output on `agent.turn.retried`.

| Event | Meaning |
| --- | --- |
| `agent.started` | A run began; carries the input. |
| `agent.finished` | The settled terminal event: no more automatic work remains. Carries `Summary`; also exposed as `agent.AgentSettled`. |
| `agent.turn.started` | A new model request began; flush buffered deltas from the previous turn. |
| `agent.turn.finished` | One model request completed. The run may still execute tools, retry, compact, or deliver queued work. |
| `agent.turn.retried` | A recoverable request failure; discard partial output for this turn before the retry's deltas. |
| `assistant.delta` | Assistant text increment. |
| `reasoning.delta` | Reasoning text increment. |
| `tool.started` | A tool is about to execute. |
| `tool.progress` | Streaming tool arguments or execution progress. |
| `tool.completed` | A tool finished successfully. |
| `tool.failed` | A tool failed, timed out, was rejected before execution, or was skipped. Carries `code`, `executed`, and `retryable`. |
| `usage` | Token accounting for an LLM call. |
| `context.compaction.started` | Iris began summarizing or trimming older context. |
| `context.compaction.finished` | The compaction pass ended, with before/after estimates and counts. |
| `agent.queue.updated` | The pending steering/follow-up queue after a delivery or a discard at run end. |
| `agent.instruction.delivered` | A queued instruction became a user message; carries its kind and text. |
| `agent.error` | A run-level error. |

`AgentFinished.Summary` is an `*agent.RunSummary` built from runtime observations. It is nil when a run fails before it begins; see [Execution Summaries](execution.md#execution-summaries) for its counters and Git fields.

```go
ag.Subscribe(func(event agent.Event) {
    switch e := event.(type) {
    case agent.AssistantDelta:
        fmt.Print(e.Text)
    case agent.ToolStarted:
        fmt.Println("tool:", e.ToolName)
    case agent.InstructionDelivered:
        fmt.Printf("%s delivered: %s\n", e.Kind, e.Text)
    case agent.QueueUpdated:
        fmt.Println("pending:", e.Steering, e.FollowUps)
    case agent.TurnRetried:
        fmt.Println("retrying turn:", e.Reason)
    case agent.TurnFinished:
        fmt.Println("request complete:", e.FinishReason, "tool calls:", e.ToolCalls)
    case agent.AgentError:
        fmt.Fprintln(os.Stderr, e.Message)
    case agent.AgentFinished:
        fmt.Println("settled, canceled:", e.Canceled)
    }
})

go func() {
    if err := ag.Run(ctx, "Implement the feature"); err != nil {
        // ctx cancellation, a budget, or a run error
    }
}()

// Correct the approach mid-run. Delivered after the current tool finishes.
if err := ag.Steer("Do not modify the database schema; use the existing structure."); err != nil {
    // agent.ErrAgentIdle when the run already ended
}

// Abort and recover instructions that were not delivered.
queued := ag.Abort()
```

`QueueUpdated` is emitted from the run goroutine. Enqueueing is acknowledged by the return value of `Steer` and `FollowUp`; it does not emit an event immediately.

Event handlers run synchronously on the run goroutine; keep them fast and do not call `Run`, `Close`, or `LoadHistory` from inside a handler. Panics in handlers are recovered. A panicking provider or tool still results in a terminal `agent.finished` event. The TUI is just one consumer of this stream.

Stable public packages:

| Package | Purpose |
| --- | --- |
| [`agent`](../agent) | Agent interface, execution control (steer/follow-up/abort), typed events, approvals, errors. |
| [`provider`](../provider) | Streaming LLM provider interface, stream events, capabilities, provider-neutral request/response types. |
| [`tool`](../tool) | Tool and registry interfaces, plus `tool.Environment` for custom tools. |
| [`runtime`](../runtime) | Constructor, runtime/security options, provider/tool composition root. |
| [`session`](../session) | Session model and JSONL manager. |
| [`resource`](../resource) | Global/project Markdown resource discovery and precedence. |
| [`client`](../client) | Session-aware headless API and event envelope. |
| [`rpc`](../rpc) | JSON-RPC 2.0 server for remote control. |
| [`provider/openai`](../provider/openai) | OpenAI-compatible provider transport. |

Implementation details stay under `internal/`.

## Errors

| Sentinel | Meaning |
| --- | --- |
| `agent.ErrAgentBusy` | A run is already active. |
| `agent.ErrAgentClosed` | The agent was closed. |
| `agent.ErrAgentIdle` | A steering instruction or follow-up was submitted while no run was active. |
| `agent.ErrEmptyInput` | A steering instruction or follow-up contained only whitespace. |
| `agent.ErrEmptyResponse` | The model returned an empty response that cannot be treated as a normal stop (for example, truncated before any content). |
| `agent.ErrTurnBudgetExceeded` | `max_turns` was exhausted. |
| `agent.ErrToolBudgetExceeded` | `max_tool_calls` was exhausted. |
| `agent.ErrToolTimeout` | Wraps a tool execution that exceeded its timeout (reported as a failed tool). |
| `agent.ErrPersistence` | A transcript recorder could not durably persist a message or tool result; the run aborted. |
| `client.ErrBusy` | A client run or mutating session operation was attempted while busy. |
| `runtime.ErrRuntimeClosed` | `NewAgent` was called after `Runtime.Close`. |
| `session.ErrSessionNotFound` | The session ID does not exist. |
| `session.ErrNoSessions` | No persisted sessions exist. |
| `session.ErrInvalidSessionID` | The session ID is unsafe or malformed. |
| `session.ErrInvalidForkPoint` | The fork target is missing or not a user message. |
| `session.ErrSessionLocked` | Another writer holds the session lease, or an append was attempted without one. |
| `session.ErrSessionConflict` | A full save was attempted with a stale revision. |
| `session.ErrUnsupportedSessionVersion` | The session uses a newer schema version than this Iris build supports. |
| `provider.ErrContextWindow` | The provider rejected a prompt for exceeding the context window. |
| `provider.ErrStreamInterrupted` | The provider stream disconnected or ended before a complete response. Partial output is not committed. |
| `provider.ErrInvalidStream` | The provider sent malformed or contradictory stream data. |
| `agent.ErrContextOverflow` | Even a compacted prompt cannot fit the configured context window. |

Use `errors.Is` with these sentinels. `provider.IsContextWindowError` also recognizes common provider error text. `provider.IsTruncatedFinish` recognizes `length`/`max_tokens` finish reasons.
