# Execution Model

[← Documentation index](README.md)

---


## How It Works


Iris runs a **deterministic agentic loop**. Each run repeats the same steps:

1. **Build a prompt view.** The context manager estimates the prompt size and compacts older turns when the conversation approaches the model window. The canonical conversation is never mutated by compaction.
2. **Call the LLM.** The request contains the prompt view, the built-in and custom tool definitions, and a `max_tokens` reservation for the next response. Retryable transport errors are retried with bounded exponential backoff; a stream that disconnects after it starts terminates the request cleanly.
3. **Validate and execute tools.** If the model returned tool calls, Iris validates and normalizes them, then runs them sequentially in order. Each call receives exactly one result (success or structured error), and invalid or truncated calls are never executed.
4. **Finish.** A turn with no tool calls ends the run, unless a steering instruction or follow-up is queued. Queued steering is delivered before each turn; queued follow-ups are delivered only when the run would otherwise stop.

At the loop level this appears as three states: `idle` (generate the next response), `tool` (execute requested tools), and `done` (stop).

The transcript is durable independently of the prompt view: the user message is journaled before the model call, assistant tool calls are journaled before any of them execute, and each tool result is journaled immediately after it returns. A crash therefore leaves an identifiable, resumable transcript rather than a half-written conversation. See [Sessions](sessions.md#sessions).

Instruction provenance is explicit. Built-in rules and user-level configuration are trusted; project instructions, repository files, shell output, and tool results are untrusted data. Project content cannot override the execution policy or security boundaries. See [Project trust and instruction provenance](resources.md#project-trust-and-instruction-provenance).

Every run has its own `context.Context`. Cancellation propagates to the provider request and stream, retry backoff, approval requests, and tool execution. Interrupting a run records synthetic tool results for tool calls that never executed so the conversation history remains valid for the next run. A run can also be steered or given follow-up work without being canceled; see [Execution Control](#execution-control).

A model request and the overall execution are distinct. `agent.turn.finished` reports that one request completed; the run may still execute tools, be retried after context compaction, or deliver queued work. `agent.finished` is the **settled** event: no automatic work remains. See [Request and execution lifecycle](#request-and-execution-lifecycle).

Runs are bounded by `max_turns` and `max_tool_calls`; each tool execution is additionally bounded by `tool_timeout_seconds`. Exhausting a turn or tool budget returns a clear error instead of looping indefinitely.

Progress is exposed as a typed event stream (`agent.started`, `agent.turn.finished`, `assistant.delta`, `tool.completed`, `context.compaction.finished`, `agent.queue.updated`, …). The TUI, JSONL mode, and RPC server are all consumers of the same stream. See [Embedding Iris in Go](embedding.md#embedding-iris-in-go).


## Execution Control


A run does not have to be all-or-nothing. Iris implements the Pi agent harness interaction model for controlling an active run: **abort**, **steering**, and **follow-up**. All three operate on the single active run; Iris does not introduce concurrent agent execution.

| Operation | Go API | RPC method | Meaning |
| --- | --- | --- | --- |
| Abort | `Cancel()`, `Abort()` | `agent.cancel` | Stop the current execution. Conversation history stays valid for a later run. |
| Steering | `Steer(text)` | `agent.steer` | Queue an instruction that influences the next turn. |
| Follow-up | `FollowUp(text)` | `agent.follow_up` | Queue a request for after the current execution finishes. |
| Clear | `ClearQueue()` | `agent.clear_queue` | Remove queued instructions and return their text. |

### Delivery semantics

- **Steering** is delivered at the next turn boundary: after the assistant turn and all tool calls it requested have finished, before the next LLM request. It never interrupts an in-flight provider request or tool execution. A steering instruction that arrives during the final assistant turn gives the run one more turn instead of being lost.
- **Follow-up** is delivered only when the run would otherwise stop: the last turn produced no tool calls and no steering instructions are pending. Delivery continues the same run, preserving conversation history and budgets.
- **Ordering.** Steering takes priority over follow-ups. All queued messages of one kind are delivered together in FIFO order.
- **Abort** cancels the run context. The provider request and stream, retry backoff, approvals, and tool execution stop. Queued instructions that have not been delivered are discarded when the run ends. `Abort()` closes the queue atomically and returns the pending text so an interactive client can restore it to its editor; `Cancel()` is the plain abort.
- **History.** Delivered instructions are ordinary user messages. They are persisted before the model request and are visible to the model on that turn. The system prompt tells the model to treat newer instructions as authoritative and abandon a superseded approach. Aborting leaves valid history: tool calls that never produced results are recorded as interrupted tool results on recovery.
- **Budgets.** Each delivered steering or follow-up message starts a new turn and counts against `max_turns`. Queued instructions do not bypass execution budgets.

For example, while Iris is implementing a feature, you can correct its approach without losing the run:

```
You> Stop. Do not modify the database schema. Use the existing structure instead.
```

In the TUI, `Enter` while streaming queues a steering instruction and `Alt+Enter` queues a follow-up. `Ctrl+C` aborts and restores queued text to the input. From RPC or Go, use the methods in the table above.


## Model Response and Tool Handling


Iris targets small local models behind OpenAI-compatible servers. The transport is deliberately tolerant of server variation but conservative about side effects: it accepts a response that closes after a `finish_reason` without `[DONE]`, but a stream that ends with neither is treated as interrupted. It never executes a tool from a partial, truncated, or malformed call.

| Scenario | Behavior |
| --- | --- |
| Incomplete streamed tool arguments | Deltas are accumulated by index, ID, or continuation. If the response ends before the call is complete, or `finish_reason` is `length`/`max_tokens`, no tool runs. |
| Invalid tool arguments | Assistant history is kept provider-valid by replacing malformed JSON arguments with `{}`. The model receives a structured `invalid_tool_arguments` result and the tool is not executed. |
| Unknown tool name | No execution. The model receives `unknown_tool`, the run continues, and the model can correct the call. |
| Multiple tool calls | Calls are assembled in order, IDs are normalized to be unique, and each is executed sequentially with one correlated result. |
| Tool execution failure | The failure is committed to conversation history as a structured tool result. The tool is not automatically retried. |
| Empty assistant response | A normal empty stop settles instead of looping; the TUI shows a notice and the turn event reports zero content. An empty response truncated at the output limit or blocked by a content filter returns `agent.ErrEmptyResponse`. |
| Context overflow | Iris proactively compacts, forces compaction when a provider rejects the prompt, and retries the request once. If the prompt still cannot fit, the run fails with a context-overflow error. |
| Stream disconnect | Partial output is not committed. A disconnect after the response starts fails with `provider.ErrStreamInterrupted`; the run terminates cleanly. |
| Malformed stream data | The request fails with `provider.ErrInvalidStream`; no partial response is committed. |
| Retryable HTTP error | The request is retried with bounded exponential backoff (1s, 2s, 4s, … capped at 30s), honoring `Retry-After`. |
| Error after the stream starts | No transport retry. A started stream is never silently replayed; it either completes or fails cleanly. |

### Provider retries vs tool retries

Only model requests are retried automatically. A retry happens before a response is consumed, or after an application-level compaction; no tool has executed at those points. Tool execution is never retried: repeating a tool that already modified a file or ran a shell command can duplicate side effects. Every failed or rejected call is reported once, and both `tool.failed` and the JSON tool result carry `executed` and `retryable` flags so the model knows whether another attempt is safe.

### Structured tool errors

A failed or rejected tool call is appended to conversation history as a JSON `tool` message, for example:

```json
{
  "ok": false,
  "error": {
    "code": "invalid_tool_arguments",
    "message": "missing required argument \"path\"",
    "tool": "read",
    "tool_call_id": "call_1",
    "executed": false,
    "retryable": true
  }
}
```

| Code | Meaning |
| --- | --- |
| `invalid_tool_call` | The call had no usable function name, or the provider content-filtered the response. |
| `invalid_tool_arguments` | Arguments were not a valid JSON object, violated the tool's top-level schema, or were rejected by the tool before execution. |
| `truncated_tool_arguments` | The response hit the output token limit, so the arguments may be incomplete. |
| `unknown_tool` | The model named a tool that is not registered. |
| `tool_execution_failed` | The tool ran and returned an error or panicked. |
| `tool_timeout` | The tool ran and exceeded `tool_timeout_seconds`. |
| `tool_result_encode_failed` | The tool ran but its result could not be encoded. |
| `run_canceled` | The run was canceled before or during execution. |
| `tool_budget_exhausted` | The per-run tool execution budget was exhausted. |

`executed: true` means the tool was invoked and may have had side effects. `retryable: true` is used only for validation failures that happened before execution. Crash recovery additionally synthesizes `tool_execution_interrupted` for an assistant tool call that has no recorded result; see [Sessions](sessions.md#durability-and-crash-recovery).

### Request and execution lifecycle

`agent.turn.started` and `agent.turn.finished` describe one model request. `agent.turn.retried` reports a recoverable failed attempt (currently a context overflow) and tells consumers to discard partial output before the retry's deltas. `agent.finished` is the **settled** terminal event: it is emitted only after tool execution, retries, compaction, steering, and follow-ups are exhausted. In Go, `agent.AgentSettled` is an alias for `agent.AgentFinished` that names this semantic.

This distinction matters to every interface: a request can complete while the execution still has more work. The TUI, JSONL, and RPC consumers receive the same `agent.turn.finished` and `agent.turn.retried` events.


## Execution Summaries


Every settled run produces a structured summary collected from runtime observations, never from a model-generated recap. The TUI shows it as the final notice, print mode writes it to stderr, and JSONL/RPC results include it.

```text
Task completed

Files changed:
  M internal/agent/runner.go
  M internal/session/store.go
  A internal/session/recovery_test.go

Tool executions: 8
LLM requests: 5
Tokens consumed: 12,450
Duration: 48s
```

The fields are:

| Field | Meaning |
| --- | --- |
| `started_at`, `finished_at`, `duration_ms` | Run timing. Duration starts when the run is admitted and ends before the Git observation. |
| `llm_requests` | Actual provider calls, including compaction summaries and context-overflow retries. |
| `tool_executions` | Tools that actually started. Invalid, skipped, and budget-exhausted calls are not counted. |
| `usage` | Token usage summed from every provider response, including summaries and retries. |
| `git_repository` | Whether the workspace was inside a Git work tree when the summary was collected. |
| `files_changed` | Working-tree status entries (`M`, `A`, `D`, `R`, `C`, `T`, `U`) with paths. |

The file list comes from `git status --porcelain` in the workspace with a short timeout. It reflects the working tree at the end of the run, so it can include changes that predated the run. For a clean repository the output says `Files changed: none`; outside a Git work tree the section is omitted. The TUI displays at most 100 files and summarizes the remainder.

The summary is available as `agent.AgentFinished.Summary` and `client.Result.Summary`, and `agent.RunSummary.Format` renders the human-readable form. It is a foundation for evaluation: the counters and file observations come from the runtime, event stream, provider usage, and Git, not from assistant text.
