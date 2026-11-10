# Command-Line Interface

[← Documentation index](README.md)

---

## Usage

```text
iris is an AI agent that can perform various tasks based on user input.

Usage:
  iris [flags]
  iris [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  jsonl       Stream structured execution events as JSONL
  print       Execute one prompt and print the final response
  rpc         Control a headless agent session over JSON-RPC

Flags:
  -b, --base-url string   Set the LLM API URL (Defaults to http://127.0.0.1:8080/v1)
  -d, --debug             Enable debug logging
  -h, --help              help for iris
  -m, --model string      Set the LLM model name (e.g. unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_XL)
  -w, --workdir string    Set the working directory for the agent (Defaults to current directory)

Use "iris [command] --help" for more information about a command.
```

The flags are persistent and accepted by every subcommand. `--model` overrides `default_model`, and `--base-url` overrides `base_url`.

The headless modes share the same agent, tools, security policy, resources, context manager, and durable sessions as the TUI. Each active session holds an exclusive writer lease while the process uses it.

## Print

```bash
iris print "Summarize the failing tests"
cat prompt.txt | iris print
```

`print` writes only the final assistant response to stdout. Diagnostics, warnings, and errors go to stderr, and the command exits non-zero on failure. If a run fails after producing partial output, that partial response is still written to stdout.

Arguments are joined with spaces into one prompt. With no argument, the prompt is read from stdin; an empty prompt is an error.

Print mode creates a new persistent session (unless persistence is disabled in an embedding). The user prompt is durably recorded before the model call; assistant messages and tool results are recorded as they complete, so a failed run still leaves a valid, resumable transcript. The execution summary is written to stderr; stdout remains the final assistant response only.

## JSONL

```bash
iris jsonl "Implement the requested change"
```

JSONL mode writes one JSON object per line. Each record has a `type` (the agent event type) and the serialized event:

```json
{"type":"agent.started","event":{"at":"...","input":"..."}}
{"type":"agent.turn.started","event":{"at":"...","turn":0}}
{"type":"assistant.delta","event":{"at":"...","text":"..."}}
{"type":"tool.started","event":{"at":"...","tool_call_id":"...","tool_name":"read","args":{"path":"main.go"}}}
{"type":"tool.completed","event":{"at":"...","tool_call_id":"...","tool_name":"read","result":{"content":"...","truncated":false},"duration":123456}}
{"type":"usage","event":{"at":"...","prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}
{"type":"agent.turn.finished","event":{"at":"...","turn":0,"attempts":1,"finish_reason":"tool_calls","tool_calls":1,"content_bytes":12}}
{"type":"context.compaction.finished","event":{"at":"...","window":75000,"before_tokens":65000,"after_tokens":18000,"summarized_messages":12}}
{"type":"agent.finished","event":{"at":"...","summary":{"llm_requests":5,"tool_executions":8,"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120},"duration_ms":48000}}}
{"type":"result","event":{"content":"...","session_id":"s-...","usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120},"summary":{"llm_requests":5,"tool_executions":8,"duration_ms":48000}}}
```

- `type` matches the typed agent event names (`agent.started`, `agent.turn.finished`, `assistant.delta`, `tool.completed`, `context.compaction.started`, …).
- `agent.turn.finished` marks one completed model request. `agent.turn.retried` precedes an in-run retry and means partial output from the failed attempt must be discarded. `agent.finished` is the settled terminal event.
- `duration` in tool events is a Go duration in nanoseconds.
- After the run starts, a final `result` record is written, including on failure. It carries `content`, optional `reasoning`, `session_id`, aggregated `usage`, optional `context_summary_usage`, optional `canceled`, optional `truncated`, and the runtime-observed `summary`. Setup errors that occur before the run (missing prompt, invalid configuration) are reported on stderr with no event records.
- On failure an `error` record (`{"type":"error","error":"..."}`) follows the `result` record, the error is also written to stderr, and the process exits non-zero.

## RPC

```bash
iris rpc
```

RPC mode speaks JSON-RPC 2.0 over stdin/stdout. Agent events are sent as `agent.event` notifications with the same `{type,event}` envelope used by JSONL.

| Method | Params | Result |
| --- | --- | --- |
| `ping` | — | `"pong"` |
| `agent.run` | `{"prompt":"..."}` | Final `client.Result` when the run completes |
| `agent.cancel` | — | `{"canceled":bool,"steering":[...],"follow_ups":[...]}` |
| `agent.steer` | `{"prompt":"..."}` | `{"queued":true}` |
| `agent.follow_up` | `{"prompt":"..."}` | `{"queued":true}` |
| `agent.clear_queue` | — | `{"steering":[...],"follow_ups":[...]}` |
| `agent.status` | — | `{"running":bool,"session_id":"...","pending_steering":n,"pending_follow_ups":n}` |
| `session.new` | — | New session metadata |
| `session.resume` | `{"id":"..."}`, or no params for the latest | Session metadata |
| `session.list` | — | Array of session summaries |
| `session.export` | — | `{"path":"..."}` |
| `session.fork` | `{"entry_id":"..."}` or `{"user_index":1}` | Forked session metadata |
| `session.current` | — | `{"id":"..."}` |
| `shutdown` | — | `{"shutdown":true}` |

`agent.run` is asynchronous so `agent.steer`, `agent.follow_up`, `agent.clear_queue`, `agent.cancel`, `agent.status`, and `shutdown` can be processed while it runs. The server starts reading the next request only after the run has started, so a `agent.steer` sent immediately after `agent.run` is deterministic. A second concurrent run is rejected with the busy error. Mutating session methods (`session.new`, `session.resume`, `session.export`, `session.fork`) are rejected as busy during a run; reads (`agent.status`, `session.list`, `session.current`) remain available.

Steering and follow-up return the idle error when no run is active. `agent.clear_queue` works while idle and while running. `agent.cancel` is an abort: it closes the queue, returns instructions that had not been delivered, and cancels the run. See [Execution Control](execution.md#execution-control) for delivery points. A session resumed through RPC is repaired under its writer lease, and another process holding that lease makes the operation fail with `session.ErrSessionLocked`.

The RPC event stream carries `agent.turn.finished`, `agent.turn.retried`, and the settled `agent.finished` event. A `agent.turn.finished` event is not the end of `agent.run`; the run may continue with tools, compaction, retries, or queued work. The final `client.Result` includes `truncated` when the last model turn hit the output token limit, and `summary` with the runtime-observed execution counters, usage, duration, and Git working-tree changes.

Errors use standard JSON-RPC codes:

| Code | Meaning |
| --- | --- |
| `-32700` | Invalid JSON |
| `-32600` | Invalid JSON-RPC request |
| `-32601` | Unknown method |
| `-32602` | Invalid params |
| `-32603` | Internal error (including run failures) |
| `-32000` | Busy |
| `-32001` | Idle: no run is active for `agent.steer` or `agent.follow_up` |

On a failed `agent.run`, the error response's `data` contains the partial `client.Result`. `shutdown` cancels the active run, responds, and the server waits for the run to finish before exiting. EOF on stdin also stops the server. `SIGINT`/`SIGTERM` cancel the server and close stdin.

Example:

```json
{"jsonrpc":"2.0","id":1,"method":"agent.run","params":{"prompt":"hello"}}
{"jsonrpc":"2.0","method":"agent.event","params":{"type":"assistant.delta","event":{"at":"...","text":"hi"}}}
{"jsonrpc":"2.0","id":1,"result":{"content":"hi","session_id":"s-...","usage":{...}}}
```

A steering exchange while `agent.run` is active:

```json
{"jsonrpc":"2.0","id":2,"method":"agent.steer","params":{"prompt":"Do not modify the schema; use the existing structure."}}
{"jsonrpc":"2.0","id":2,"result":{"queued":true}}
{"jsonrpc":"2.0","method":"agent.event","params":{"type":"agent.instruction.delivered","event":{"at":"...","kind":"steering","text":"Do not modify the schema; use the existing structure."}}}
```

## Headless approvals

There is no interactive TUI approval prompt in headless modes. Shell commands that require approval fail closed unless an allowlist, approval hook, or custom approver is configured. This is deliberate: `print`, `jsonl`, and `rpc` must not silently approve risky commands.

Go embedders can install an approver through `runtime.Options.Approver` or `agent.SetApprover`. See [Security and Permissions](security.md#security-and-permissions).
