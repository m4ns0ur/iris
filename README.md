# Iris

**Iris** is a local-first AI agent built in Golang, designed to assist with software development and evolve into a general-purpose autonomous agent. It is optimized specifically for small open models (~9B parameters) running on local hardware.

## Table of Contents

- [How It Works](#how-it-works)
- [Execution Control](#execution-control)
- [Requirements](#requirements)
- [Quick Start](#quick-start)
- [CLI](#cli)
- [Configuration Reference](#configuration-reference)
- [Security and Permissions](#security-and-permissions)
- [Built-in Tools](#built-in-tools)
- [TUI](#tui)
- [Sessions](#sessions)
- [Resources](#resources)
- [Context Management](#context-management)
- [Embedding Iris in Go](#embedding-iris-in-go)
- [Development](#development)
- [My Setup](#my-setup)
- [Why Iris Exists](#why-iris-exists)
- [Similar Projects](#similar-projects)
- [License](#license)

## How It Works

Iris runs a **deterministic agentic loop**. Each run repeats the same steps:

1. **Build a prompt view.** The context manager estimates the prompt size and compacts older turns when the conversation approaches the model window. The canonical conversation is never mutated by compaction.
2. **Call the LLM.** The request contains the prompt view, the built-in and custom tool definitions, and a `max_tokens` reservation for the next response.
3. **Execute tools.** If the model returned tool calls, Iris runs them (sequentially, in order), appends each tool result to the conversation, and begins another turn.
4. **Finish.** A turn with no tool calls ends the run, unless a steering instruction or follow-up is queued. Queued steering is delivered before each turn; queued follow-ups are delivered only when the run would otherwise stop.

At the loop level this appears as three states: `idle` (generate the next response), `tool` (execute requested tools), and `done` (stop).

Every run has its own `context.Context`. Cancellation propagates to the provider request and stream, retry backoff, approval requests, and tool execution. Interrupting a run records synthetic tool results for tool calls that never executed so the conversation history remains valid for the next run. A run can also be steered or given follow-up work without being canceled; see [Execution Control](#execution-control).

Runs are bounded by `max_turns` and `max_tool_calls`; each tool execution is additionally bounded by `tool_timeout_seconds`. Exhausting a turn or tool budget returns a clear error instead of looping indefinitely.

Progress is exposed as a typed event stream (`agent.started`, `assistant.delta`, `tool.completed`, `context.compaction.finished`, `agent.queue.updated`, …). The TUI, JSONL mode, and RPC server are all consumers of the same stream. See [Embedding Iris in Go](#embedding-iris-in-go).

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
- **History.** Delivered instructions are ordinary user messages. They are persisted with the session and are visible to the model on that turn. The system prompt tells the model to treat newer instructions as authoritative and abandon a superseded approach. Aborting leaves valid history: tool calls that never produced results get synthetic tool results.
- **Budgets.** Each delivered steering or follow-up message starts a new turn and counts against `max_turns`. Queued instructions do not bypass execution budgets.

For example, while Iris is implementing a feature, you can correct its approach without losing the run:

```
You> Stop. Do not modify the database schema. Use the existing structure instead.
```

In the TUI, `Enter` while streaming queues a steering instruction and `Alt+Enter` queues a follow-up. `Ctrl+C` aborts and restores queued text to the input. From RPC or Go, use the methods in the table above.

## Requirements

- **Go 1.26.1+** (see `go.mod`)
- **LLM server**: any OpenAI-compatible API endpoint (llama.cpp, Ollama, vLLM, LM Studio, or a hosted compatible API)
- **Terminal**: the TUI requires a terminal emulator (iTerm2, Alacritty, Ghostty, …). Headless modes do not.

On Unix, shell commands and approval hooks run in their own process group and are killed as a group on timeout or cancellation. On other platforms Iris can only terminate the direct child process.

## Quick Start

### 1. Build

```bash
make build
```

The default `make` target runs lint first, then build. See [Development](#development) for all targets.

### 2. Start an LLM server

For example, with llama.cpp:

```bash
llama-server -m /path/to/model.gguf --port 8080 --ctx-size 75000
```

Any OpenAI-compatible endpoint works; set `base_url` accordingly (Ollama, for example, exposes `http://127.0.0.1:11434/v1`).

### 3. Configure Iris

On first run Iris creates `~/.iris/settings.toml` containing the defaults, but no models. Add at least a `default_model` and a matching `[[models]]` entry:

```toml
base_url = 'http://127.0.0.1:8080/v1'
max_retries = 3
default_model = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'

[[models]]
name = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'
context_size = 75000
# Descriptive capabilities. See "Providers" in Embedding Iris in Go.
tool_calling = true
reasoning = true
structured_output = false
vision = false
sampling_parameters = ['temperature', 'top_p', 'top_k']
temperature = 0.6
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 0.0
frequency_penalty = 0.0

[[models]]
name = 'unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 1.5

[security]
# read-only | workspace-write | danger-full-access
mode = 'workspace-write'
# Acknowledge that Iris runs inside a container/VM. Required for shell mode 'allow'.
external_sandbox = false
# read_paths = ['/some/shared/read-only-dir']
# write_paths = ['/some/shared/writable-dir']

[security.shell]
# deny | allowlist | approval | allow
mode = 'approval'
# Commands/patterns that skip approval. Patterns with spaces match the command
# or one segment; patterns without spaces match executable names.
allow = ['ls', 'pwd', 'git status*', 'git diff*', 'git log*']
# Always denied, even if approved.
deny = ['curl*', 'wget*']
# Remove the built-in catastrophic-command deny list (explicit opt-out).
# disable_default_deny = true
timeout_seconds = 120
approval_timeout_seconds = 300
max_output_bytes = 25600
max_output_lines = 1000
max_capture_bytes = 10485760
temp_file_ttl_minutes = 1440
# Optional external approval hook (argv, no shell). Exit 0 = allow.
# approval_hook = ['/usr/local/bin/iris-approve']
# Optional shell binary. Empty uses bash when available, otherwise sh.
# binary = '/bin/bash'

[session]
# Empty uses ~/.iris/sessions.
dir = ''

[context]
# Zero uses the selected model's context_size.
window = 0
# Reserved for the next assistant response and used as the request output cap.
reserve_tokens = 8192
# Compact when prompt tokens exceed this fraction of (window - reserve_tokens).
compact_at = 0.85
# Recent conversation kept verbatim during compaction.
keep_recent_tokens = 16384
# Output budget for generated summaries.
summary_max_tokens = 2048

[llm]
# Deadline for one complete request, including retries and streaming.
request_timeout_seconds = 1800

[agent]
# Per-run execution budgets.
max_turns = 100
max_tool_calls = 200
tool_timeout_seconds = 300
```

### 4. Run

```bash
./bin/iris
```

Or with CLI flags:

```bash
./bin/iris --debug --workdir ./my-project \
  --base-url http://127.0.0.1:11434/v1 \
  --model model-name
```

`--workdir` sets the workspace used by tools, shell commands, and project resources. It does not change the process working directory, so `iris_debug.log` is still written where Iris was launched.

## CLI

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

The headless modes share the same agent, tools, security policy, resources, context manager, and persistent sessions as the TUI.

### Print

```bash
iris print "Summarize the failing tests"
cat prompt.txt | iris print
```

`print` writes only the final assistant response to stdout. Diagnostics, warnings, and errors go to stderr, and the command exits non-zero on failure. If a run fails after producing partial output, that partial response is still written to stdout.

Arguments are joined with spaces into one prompt. With no argument, the prompt is read from stdin; an empty prompt is an error.

Print mode creates a new persistent session (unless persistence is disabled in an embedding) and saves it after the run attempt.

### JSONL

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
{"type":"context.compaction.finished","event":{"at":"...","window":75000,"before_tokens":65000,"after_tokens":18000,"summarized_messages":12}}
{"type":"agent.finished","event":{"at":"..."}}
{"type":"result","event":{"content":"...","session_id":"s-...","usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}}
```

- `type` matches the typed agent event names (`agent.started`, `assistant.delta`, `tool.started`, `context.compaction.started`, …).
- `duration` in tool events is a Go duration in nanoseconds.
- After the run starts, a final `result` record is written, including on failure. It carries `content`, optional `reasoning`, `session_id`, aggregated `usage`, optional `context_summary_usage`, and optional `canceled`. Setup errors that occur before the run (missing prompt, invalid configuration) are reported on stderr with no event records.
- On failure an `error` record (`{"type":"error","error":"..."}`) follows the `result` record, the error is also written to stderr, and the process exits non-zero.

### RPC

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

Steering and follow-up return the idle error when no run is active. `agent.clear_queue` works while idle and while running. `agent.cancel` is an abort: it closes the queue, returns instructions that had not been delivered, and cancels the run. See [Execution Control](#execution-control) for delivery points.

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

### Headless approvals

There is no interactive TUI approval prompt in headless modes. Shell commands that require approval fail closed unless an allowlist, approval hook, or custom approver is configured. This is deliberate: `print`, `jsonl`, and `rpc` must not silently approve risky commands.

Go embedders can install an approver through `runtime.Options.Approver` or `agent.SetApprover`. See [Security and Permissions](#security-and-permissions).

## Configuration Reference

Configuration is TOML, loaded from `~/.iris/settings.toml`. Iris creates the file with defaults on first run. Missing files, directories, and resources are not errors.

Precedence (highest first):

1. CLI flags (`--base-url`, `--model`)
2. `IRIS_*` environment variables
3. `~/.iris/settings.toml`
4. Built-in defaults

### Top-level

| Key | Default | Purpose |
| --- | --- | --- |
| `base_url` | `http://127.0.0.1:8080/v1` | OpenAI-compatible API root. `--base-url` overrides. |
| `max_retries` | `3` | HTTP attempts for one LLM request (minimum 1). |
| `default_model` | `""` | Model sent with requests and matched against `[[models]]`. `--model` overrides. |
| `[[models]]` | — | One or more model settings blocks. |

### Model settings (`[[models]]`)

`default_model` is matched case-insensitively against `name`. If no entry matches, Iris logs an error and uses zero-valued settings. Because `--model` replaces `default_model`, passing a model that is not listed in `[[models]]` also produces zero settings: the context window falls back to the default and transport sampling defaults apply.

| Key | Default | Purpose |
| --- | --- | --- |
| `name` | — | Model name sent to the provider. |
| `context_size` | `0` | Model context window used by the context manager. `context.window` overrides it when set; if both are zero, 32768 is used. |
| `tool_calling` | `false` | Descriptive capability metadata (`provider.Capabilities`). |
| `reasoning` | `false` | Descriptive capability metadata. |
| `structured_output` | `false` | Descriptive capability metadata. |
| `vision` | `false` | Descriptive capability metadata. |
| `sampling_parameters` | `[]` | Descriptive list of sampling parameters the model supports. Metadata only. |
| `temperature` | `0` (transport default `0.6`) | Sampling temperature. Zero is treated as unset. |
| `top_p` | `0` (omitted) | Nucleus sampling. Zero is omitted from the request. |
| `top_k` | `0` (omitted) | Top-k sampling. Zero is omitted. |
| `min_p` | `0` (omitted) | Min-p sampling. Zero is omitted. |
| `repeat_penalty` | `0` (omitted) | Repeat penalty. Zero is omitted. |
| `presence_penalty` | `0` (omitted) | Presence penalty. Zero is omitted. |
| `frequency_penalty` | `0` (omitted) | Frequency penalty. Zero is omitted. |

The capability flags and `sampling_parameters` are descriptive only: providers must not assume a feature is supported just because it was declared. Sampling values and `context_size` do affect requests and context management.

### Security (`[security]`)

| Key | Default | Purpose |
| --- | --- | --- |
| `mode` | `workspace-write` | `read-only`, `workspace-write`, or `danger-full-access`. See [File sandbox](#file-sandbox). |
| `external_sandbox` | `false` | Acknowledge that Iris runs inside a container/VM. Required for shell mode `allow`. |
| `read_paths` | `[]` | Additional paths the file tools may read. |
| `write_paths` | `[]` | Additional paths the file tools may write (and therefore read). |
| `shell` | — | Shell policy, described below. |

### Shell (`[security.shell]`)

| Key | Default | Purpose |
| --- | --- | --- |
| `mode` | `approval` | `deny`, `allowlist`, `approval`, or `allow`. See [Shell policy](#shell-policy). |
| `allow` | `[]` | Glob patterns for commands that skip approval. |
| `deny` | `[]` | Glob patterns that are always denied. |
| `disable_default_deny` | `false` | Remove the built-in catastrophic-command deny list. |
| `approval_hook` | `[]` | argv of an external approval program (no shell). |
| `timeout_seconds` | `120` | Timeout for one shell command. |
| `approval_timeout_seconds` | `300` | How long Iris waits for an approval decision. |
| `max_output_bytes` | `25600` (25 KiB) | Model-visible output cap. Maximum 4 MiB. |
| `max_output_lines` | `1000` | Model-visible line cap. Maximum 200,000. |
| `max_capture_bytes` | `10485760` (10 MiB) | Full output written to a temp file. Maximum 100 MiB. |
| `temp_file_ttl_minutes` | `1440` (24 h) | Reaping age for temp directories left by crashed processes. |
| `binary` | `""` | Shell used for commands. Empty uses `bash` when available, otherwise `sh`. |

`max_capture_bytes` is raised to at least `max_output_bytes` if configured lower. Output limits above the maxima are rejected at startup.

### LLM, agent, context, and sessions

| Key | Default | Purpose |
| --- | --- | --- |
| `llm.request_timeout_seconds` | `1800` | Deadline for one complete LLM request, including retries and streaming. |
| `agent.max_turns` | `100` | LLM turns per run. |
| `agent.max_tool_calls` | `200` | Tool executions per run. |
| `agent.tool_timeout_seconds` | `300` | Timeout for one tool execution. |
| `context.window` | `0` | Overrides the model `context_size`. Zero uses the model setting. |
| `context.reserve_tokens` | `8192` | Reserved for the next response; also sent as `max_tokens`. |
| `context.compact_at` | `0.85` | Fraction of the prompt budget that triggers compaction. |
| `context.keep_recent_tokens` | `16384` | Recent conversation kept verbatim during compaction. |
| `context.summary_max_tokens` | `2048` | Output budget for compaction summaries. |
| `session.dir` | `""` | Session store directory. Empty uses `~/.iris/sessions`. |

### Environment variables

Every known configuration key can be overridden with an `IRIS_`-prefixed environment variable; dots become underscores and keys are upper-cased. For example:

```bash
IRIS_BASE_URL=http://127.0.0.1:11434/v1
IRIS_DEFAULT_MODEL='unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
IRIS_SECURITY_MODE=read-only
IRIS_SECURITY_SHELL_MODE=deny
IRIS_SECURITY_SHELL_TIMEOUT_SECONDS=60
IRIS_AGENT_MAX_TURNS=50
IRIS_CONTEXT_RESERVE_TOKENS=4096
```

Viper resolves environment variables for keys that are known through the config file or built-in defaults. The `[[models]]` list is not set through environment variables.

## Security and Permissions

Iris deliberately does not ship a full permission UI framework. It provides a small, explicit execution boundary, a one-line approval prompt for the TUI, and extension points, in the spirit of Pi's container-first approach.

### File sandbox

In `read-only` and `workspace-write` modes, every file tool resolves the requested path, follows symlinks, and checks the result against the configured roots. `..` escapes, symlinks that point outside the workspace, dangling symlinks, symlink loops, non-directory path components, NUL bytes, and special files are rejected. The default mode is `workspace-write`:

| `security.mode` | Reads | Writes | Shell |
| --- | --- | --- | --- |
| `read-only` | inside workspace | denied | denied |
| `workspace-write` (default) | inside workspace | inside workspace | per shell policy |
| `danger-full-access` | anywhere | anywhere | per shell policy |

`read-only` denies all writes and disables the shell tool, regardless of `security.shell.mode`.

Extra roots can be granted with `security.read_paths` and `security.write_paths`. Write roots are also readable. The workspace itself is the `--workdir` directory (or the current directory) and is canonicalized at startup.

In `danger-full-access` mode relative paths are still resolved against the workspace, but paths are not canonicalized or confined. Use it only when the process is already isolated.

The read tool is also granted read-only access to Iris' own temp directory so it can inspect full shell output. Nothing else in that directory is writable by the file tools.

Path checks are resolved before use (including symlinks), but they are not a defence against a hostile process racing Iris between the check and the write. Treat the workspace as trusted input and use an external sandbox for untrusted repositories.

### Shell policy

`security.shell.mode` controls command execution:

| Mode | Behaviour |
| --- | --- |
| `deny` | The shell tool is disabled. |
| `allowlist` | Only commands matching `security.shell.allow` run; everything else is denied. |
| `approval` (default) | Allowlisted commands run directly. Everything else requires an approval decision. |
| `allow` | Every command that does not match a deny rule runs. Requires `security.external_sandbox = true`. |

Mode aliases are accepted for convenience: `ask` (approval), `disabled`/`off` (deny), `allow-list` (allowlist), and `all`/`yolo` (allow).

Deny rules are evaluated first and cannot be overridden by approval. A small built-in deny list (privilege escalation, disk formatting, root deletion, fork bombs) is applied by default and can be removed with `security.shell.disable_default_deny = true`.

Allow and deny patterns are globs: patterns with whitespace match the whole command or one command segment, patterns without whitespace match executable names (including basename matches for absolute paths). Commands containing command substitution, subshells, heredocs, shell wrappers (`bash -c`, `eval`, `env`, `xargs`, …), or interpreters are treated as opaque and are never allowlisted. In `approval` mode they require approval; in `allowlist` mode they are denied.

Allowlisting a build tool or test runner (for example `go`) is equivalent to allowing it to execute project code; tools such as `git` can also run hooks or aliases. Interpreters such as `python`, `node`, `ruby`, and `perl` are opaque and cannot be allowlisted at all. Keep the allowlist narrow and read-only where possible.

### Approvals

Approval can be provided in three ways:

1. **Interactive TUI** (default when no approver is configured): a one-line
   `[y] allow once · [n] deny` bar is shown above the input box. `y`/`Y` allows, `n`/`N`/`Esc` denies, and `Ctrl+C` denies and cancels the run.
2. **Approval hook**: set `security.shell.approval_hook` to an argv list. Iris sends `{kind, command, workdir, programs, reason}` as JSON on stdin and treats exit status `0` as allow, any non-zero as deny. Stdout/stderr are used as the denial reason and are capped at 64 KiB. The hook runs directly (never through a shell), inherits `IRIS_APPROVAL_KIND` and `IRIS_WORKDIR`, and is bounded by `approval_timeout_seconds`.
3. **Go embedders**: implement `agent.Approver` (or use `agent.ApproverFunc`) and install it with `Agent.SetApprover` or `runtime.Options.Approver`.

If no approval mechanism is available (for example in a non-interactive embedding), commands that need approval fail closed. Approval requests are bounded by `approval_timeout_seconds` (default 5 minutes). The TUI mirrors that deadline so a timed-out prompt disappears instead of blocking the next turn.

### Timeouts, output, and temp files

- Shell commands run in their own process group on Unix and are killed (including children) when `timeout_seconds` expires or the context is cancelled. On other platforms only the direct child is killed.
- A non-zero exit code is not a tool error: the result always contains `output`, `exit_code`, `success`, `truncated`, and `duration_ms`; `timed_out`, `canceled`, and `error` appear when relevant.
- Output returned to the model is bounded to the last `max_output_bytes` / `max_output_lines` (default 25 KiB / 1000 lines).
- Up to `max_capture_bytes` (default 10 MiB) of full output is written to a `0600` file in a private session directory. The path is returned only when the model-visible output was truncated, and the read tool can read it. When output was not truncated, the temp file is removed.
- Session directories are created under the user cache directory (`.../iris/tmp/session-*`, falling back to the system temp directory) with `0700` permissions, and are removed on exit. Directories left by crashed processes are reaped after `temp_file_ttl_minutes` (default 24 hours).

### Cancellation and execution budgets

Each agent run has its own `context.Context`. Cancellation propagates to the LLM HTTP request, stream reading, retry backoff, approval requests, and tool execution. Interrupting a tool call records synthetic tool results for the remaining calls so the conversation history stays valid for the next run.

- `[llm] request_timeout_seconds` bounds a complete LLM request, including retries and streaming (default 30 minutes). Retry backoff is cancellation-aware, and non-transient 4xx responses are not retried.
- `[agent] max_turns` bounds LLM turns per run (default 100). Delivered steering instructions and follow-ups count as turns.
- `[agent] max_tool_calls` bounds tool executions per run (default 200).
- `[agent] tool_timeout_seconds` bounds a single tool execution (default 5 minutes).
- Exhausting a turn or tool budget returns a clear error instead of looping indefinitely. A tool timeout is reported as a failed tool result and the run continues.
- Tool panics are recovered and converted into tool errors so a buggy tool cannot crash the agent.

Press `Ctrl+C` while Iris is streaming to abort the current run; queued instructions are restored to the input, and pressing `Ctrl+C` again quits. `SIGTERM` exits the TUI, cancels the active run, and waits for tool and temp-file cleanup before the process exits. `Agent.Close` cancels, waits for cleanup, and removes captured temp files. Steering and follow-up are separate from cancellation and are described in [Execution Control](#execution-control).

### Edits are validated before they are applied

The edit tool requires every `find` string to occur exactly once in the original file and rejects overlapping edits. Files are written atomically via a temp file and rename, preserving permissions. The path is re-resolved immediately before writing; if it changed (for example a symlink was swapped), the edit is refused. Failed validation leaves the file untouched.

### External sandbox requirement

The shell policy is a gate, not an OS sandbox. A permitted shell command can read and write anything the Iris process can, including paths outside the workspace. The file sandbox does not constrain shell commands. For a hard boundary, run Iris inside a container or VM and set:

```toml
[security]
external_sandbox = true
```

Iris prints warnings at startup and tells the model about this limitation when `external_sandbox` is not acknowledged. `security.shell.mode = 'allow'` is refused unless the external sandbox is acknowledged (a read-only file mode disables the shell regardless).

Iris also warns about `danger-full-access`, `security.shell.mode = 'allow'`, and `disable_default_deny = true`. Warnings are written to the debug log and stderr at startup, and the policy summary is included in the system prompt.

### Logging and sensitive data

Iris writes `iris_debug.log` in the process working directory (not `--workdir`), in append mode with `0600` permissions. The log is JSON lines. `--debug` enables debug-level logging; otherwise only info and above are written. Debug logs include LLM payload previews and tool output, so treat the file as sensitive and avoid sharing it.

## Built-in Tools

| Tool | Required args | Optional args | Result keys |
| --- | --- | --- | --- |
| `read` | `path` | `start`, `max` | `content`, `truncated`, `start_line`, `lines`, `end_line` |
| `edit` | `path`, `edits` | — | `success`, `edits`, `changed` |
| `write` | `path`, `content` | — | `success`, `bytes` |
| `shell` | `command` | — | `output`, `exit_code`, `success`, `truncated`, `duration_ms`, plus `timed_out`, `canceled`, `error`, `full_output_file`, `full_output_truncated`, `full_output_bytes` when relevant |

### `read`

Reads a file inside the workspace. `start` is 1-based; `max` defaults to 1000 lines and is capped at 10,000. The result is also capped at 25 KiB. Use `start`/`max` to page through larger files. Directories, special files, and paths outside the allowed roots are rejected.

### `edit`

Applies one or more exact text replacements:

```json
{
  "path": "internal/foo.go",
  "edits": [
    {"find": "old text", "replace": "new text"},
    {"find": "another unique line", "replace": ""}
  ]
}
```

Each `find` must match exactly once in the original file and edits must not overlap. `replace` may be empty. If the replacements produce identical content, the result is `changed: false`. The file is written atomically with its existing permissions.

### `write`

Writes or creates a file inside the workspace, creating parent directories as needed. Existing files keep their permissions; new files are created with `0644`. Writes are atomic.

### `shell`

Runs `<security.shell.binary> -c <command>` in the workspace, subject to the shell policy. The default shell is `bash` when available, otherwise `sh`; override it with `security.shell.binary`. Shell commands are not confined by the file sandbox.

### Custom tools

Implement the public `tool.Tool` interface and register the tool through `runtime.Options.Tools`:

```go
type Tool interface {
    Name() string
    Description() string
    FullDescription() string
    Parameters() provider.Parameters
    Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}
```

```go
func NewMyTool() *MyTool { return &MyTool{} }

func (t *MyTool) Name() string { return "my-tool" }
func (t *MyTool) Description() string { return "Does something useful" }
func (t *MyTool) FullDescription() string { return "Does something useful. Use when..." }
func (t *MyTool) Parameters() provider.Parameters {
    return provider.Parameters{
        Type: "object",
        Required: []string{"input"},
        Properties: map[string]provider.Property{
            "input": {Type: "string", Description: "The input"},
        },
    }
}
func (t *MyTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
    // Honour ctx cancellation in long-running custom tools.
    // Custom tools that touch the filesystem can reuse the workspace sandbox:
    if env, ok := tool.FromContext(ctx); ok {
        path, err := env.ResolveRead("some/file")
        _, _ = path, err
    }
    return map[string]any{"result": "done"}, nil
}
```

Custom tools are registered after the built-ins. Names must be unique; nil tools are skipped and an empty name or duplicate name is an error. The tool's `Description` is listed in the system prompt; its `FullDescription` and `Parameters` are sent to the provider as the tool schema. The context passed to `Execute` carries the active `tool.Environment`, which embedders can also construct with `tool.WithEnvironment`, so custom tools can resolve paths with the same policy as the built-in tools.

## TUI

The TUI is one consumer of the agent event stream. On launch it creates a new session, shows its ID, displays resource warnings, and wires an interactive approver when no approver is configured.

### Key bindings

| Binding | Action |
| --- | --- |
| `Enter` | Send the prompt or run a `/command` |
| `Enter` (streaming) | Queue a steering instruction for the next turn |
| `Alt+Enter` (streaming) | Queue a follow-up for when the run finishes |
| `Shift+Enter` or `Ctrl+J` | Insert a newline |
| `PgUp` / `PgDn` or `Ctrl+U` / `Ctrl+D` | Scroll history (idle and streaming) |
| Mouse wheel | Scroll history |
| `Ctrl+C` (idle) | Quit |
| `Ctrl+C` (streaming) | Abort the current run and restore queued instructions to the input; press again to quit |
| `y` / `Y` (approval pending) | Allow once |
| `n` / `N` / `Esc` (approval pending) | Deny |
| `Ctrl+C` (approval pending) | Deny, abort the run, and restore queued instructions; press again to quit |

The input box stays editable while Iris is streaming. `Enter` queues a steering instruction (delivered after the current assistant turn finishes its tool calls, before the next LLM request) and `Alt+Enter` queues a follow-up (delivered only when the run would otherwise stop). Queued instructions are shown in the status bar and as `You (steer)>` / `You (follow-up)>` messages when delivered. Command completion and delivery semantics are described in [Execution Control](#execution-control).

The status line shows the current hints, the scroll percentage, and the total tokens reported by the latest LLM call. While streaming it shows the active phase (`thinking`, `reasoning`, `tooling`, `compacting`, `canceling`) and any queued instruction counts; when the history is scrolled up it shows a scroll hint.

### Commands

| Command | Purpose |
| --- | --- |
| `/new` | Create a new session. |
| `/resume [id]` | Resume the latest session or one matching an ID prefix. |
| `/fork [n]` | Fork from the nth user message (1-based); omit `n` for the latest user message. |
| `/export` | Export the current session as Markdown under the session directory. |
| `/clear-queue` | Discard queued steering/follow-up instructions and restore their text to the input. Available while streaming. |
| `/skills` | List discovered skills. |
| `/skill <name>` | Insert a skill into the input box. Add your request after it. |
| `/prompts` | List prompt templates. |
| `/prompt <name> [input]` | Render a template into the input box for editing. |
| `/help`, `/?` | List commands. |

Unknown commands produce a notice. Session-mutating commands persist the current session first and are unavailable while a run is active; `/clear-queue` and `/help` remain available. The TUI saves the session after each run (including failed runs with partial history), before switching sessions, and when quitting while idle.

## Sessions

Sessions are persisted as one JSONL file per conversation under `~/.iris/sessions` by default. Override the location with:

```toml
[session]
dir = '/path/to/sessions'
```

The session directory is created with `0700` permissions and session files with `0600`. Saves rewrite the file atomically via a temp file and rename.

The first record is session metadata; subsequent records are conversation entries:

```json
{"type":"session","session":{"id":"s-...","created_at":"...","updated_at":"...","model":"...","model_metadata":{...},"workspace":"...","usage":{...}}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"user","created_at":"...","content":"..."}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"assistant","content":"...","reasoning_content":"...","tool_calls":[...],"usage":{...},"model":"..."}}
{"type":"entry","entry":{"id":"e-...","session_id":"s-...","parent_id":"...","kind":"tool","content":"...","tool_call_id":"...","tool_name":"..."}}
```

Session metadata includes the ID, timestamps, model, model metadata (provider, base URL, model context size, effective context window, configured reserve tokens, non-zero sampling values), workspace, parent/fork information, and aggregate token usage.

Entries have IDs and `parent_id` links. Message history is reconstructed from these entries when a session is resumed. System prompts are not persisted; the agent adds its current system prompt when history is loaded. Resuming and forking restore conversation history and metadata; new turns use the provider and model configured for the current Iris launch.

Steering instructions and follow-ups that were delivered during a run are ordinary user messages, so they are persisted as `user` entries. Instructions that were still queued when the run ended are not part of the conversation and are not persisted.

Session IDs use letters, digits, `-`, and `_`, up to 128 characters.

### Exports

`/export` and `session.export` write Markdown to `<session-dir>/exports/<session-id>.md` (directory `0700`, file `0600`, atomic write). The export includes session metadata, token totals, model metadata, and each entry with reasoning sections, tool calls, and tool results.

### Client and RPC session behavior

- `print`, `jsonl`, and `rpc` create a new session at startup unless an initial session is supplied by an embedding.
- Sessions are saved after each run attempt. An attempt that adds no conversation messages does not create a file.
- Delivered steering instructions and follow-ups are saved as user entries; queued instructions that were never delivered are not.
- `session.new` and `session.resume` persist the current session before switching.
- `session.fork` creates a child session containing entries through the selected user message and records `parent_session_id` and `forked_from_entry_id`.
- Mutating session methods return busy errors during a run; see [RPC](#rpc).

### `session.Manager`

The public `session` package defines the storage abstraction used by the client and TUI:

```go
type Manager interface {
    Create(options CreateOptions) (*Session, error)
    Save(session *Session) error
    Open(id string) (*Session, error)
    Latest() (*Session, error)
    List() ([]Summary, error)
    Fork(sessionID, fromEntryID string) (*Session, error)
    ExportMarkdown(session *Session) (string, error)
}
```

`session.NewJSONL(dir)` returns the default JSONL implementation. `Create` builds an unsaved session; `Save` publishes it. Forking validates that the target entry exists and is a user message.

## Resources

Iris loads Markdown instruction resources from a global directory and the current project. `AGENTS.md` is intentionally project-only: it describes the directory the agent is working in, so a global copy would not make sense.

```text
~/.iris/
├── SYSTEM.md
├── skills/
│   ├── coding/SKILL.md
│   └── debugging.md
└── prompts/
    └── review.md

project/
├── AGENTS.md
└── .iris/
    ├── AGENTS.md
    ├── SYSTEM.md
    ├── skills/
    │   └── coding/SKILL.md
    └── prompts/
        └── review.md
```

| Resource | Global | Project | Precedence |
| --- | --- | --- | --- |
| `AGENTS.md` | Not loaded | `<workdir>/AGENTS.md`, `<workdir>/.iris/AGENTS.md` | Project-only. Root instructions are followed by `.iris` instructions. |
| `SYSTEM.md` | `~/.iris/SYSTEM.md` | `<workdir>/.iris/SYSTEM.md` | The project file replaces the global file. Its content is appended as custom system instructions; Iris' built-in tool and security instructions remain. |
| Skills | `~/.iris/skills/` | `<workdir>/.iris/skills/` | Markdown only. Project skills replace global skills with the same name. |
| Prompt templates | `~/.iris/prompts/` | `<workdir>/.iris/prompts/` | Markdown only. Project prompts replace global prompts with the same name. |

### Skills

- `skills/name/SKILL.md` and `skills/name.md` both load as skill `name`.
- Nested files such as `skills/name/advanced.md` load as `name/advanced`.
- Skills and prompts may start with an optional simple front matter block:

  ```markdown
  ---
  name: coding
  description: Project coding conventions
  ---
  # Coding

  Use the project's conventions.
  ```

  `name` overrides a skill's path-derived name. `description` is shown in listings and is truncated to 160 characters. When no description is present, Iris derives one from the first non-empty body line. Prompt names always come from their file path, but prompts still honor a front matter description.

### Prompt templates

- `prompts/review.md` loads as prompt `review`; nested files use their relative path without `.md`.
- `{{input}}` is replaced with the trimmed input; all occurrences are replaced.
- If there is no `{{input}}` placeholder, non-empty input is appended after the template.

### Limits and warnings

Skills and prompt templates are Markdown data, not executable plugins. Hidden files/directories and symlinks are ignored, files over 512 KiB are skipped with a warning, and missing resource directories are not an error. Duplicate names within a scope keep the first file. A project resource overriding a global one produces a warning. Loader warnings are written to the log, printed to stderr at startup, and shown in the TUI.

Skill and prompt lookups first try an exact name, then a case-insensitive match.

## Context Management

Iris builds a prompt view for each model call instead of mutating the canonical conversation. The full, uncompressed conversation remains in the agent history and therefore in the persistent session. Compaction only affects what is sent to the model for that request.

The effective context window is selected in this order:

1. `context.window`
2. the selected model's `context_size`
3. a conservative default of 32768 tokens

`context.reserve_tokens` is subtracted from the window to form the prompt budget. It is also sent as the request's `max_tokens`, after normalization (see the notes below), so the next assistant response cannot consume the space needed by the prompt.

Before each call, the context manager:

- estimates prompt tokens from message text, tool calls, and tool schemas;
- calibrates the estimate with the provider's reported prompt-token usage (the correction factor is clamped and smoothed);
- triggers compaction when the calibrated prompt exceeds `compact_at` of the prompt budget;
- preserves leading system messages, including any project instructions;
- keeps a recent conversation tail (`keep_recent_tokens`) verbatim;
- summarizes older turns with the same provider, instructing the summarizer to preserve user constraints, decisions, file paths, commands, errors, and important tool outcomes;
- appends the summary to the last leading system message (or inserts a system message if there is none);
- uses a deterministic fallback summary if summarization fails;
- trims the largest recent tool results if the current turn alone is still too large.

If the provider rejects a prompt as exceeding its context window, Iris forces an aggressive compaction and retries once. Provider implementations should wrap `provider.ErrContextWindow` when they can classify this error; Iris also recognizes common provider error text. If the compacted prompt still cannot fit the budget, the run fails with a context-overflow error.

Context settings are normalized before use:

- the window is at least 1024 tokens;
- `reserve_tokens` is reduced to a quarter of the window when it would leave no prompt budget, and is at least 128;
- `compact_at` is clamped to `0.5`–`0.95`;
- `keep_recent_tokens` is clamped to at most the prompt budget and at least a quarter of it;
- `summary_max_tokens` is capped at half the prompt budget and is at least 128.

The normalized reserve, not the raw configured value, is what is sent as `max_tokens`.

Compaction is observable through `context.compaction.started` and `context.compaction.finished`. The finished event reports before/after estimates, summarized message count, trimmed tool-result count, summary token usage, whether the summary was a fallback, and whether compaction was forced. The TUI shows a live "Compacting context..." status while the pass runs and a notice with before/after estimates when it finishes. Summary token usage is included in the session aggregate. Session files still contain the full original conversation, so resuming a session never loses the uncompressed history.

## Embedding Iris in Go

Iris can be embedded without the CLI. The `client` package adds session-aware runs and JSON-friendly event envelopes; `runtime` wires a provider, tools, security policy, and budgets together; `agent` is the lower-level event-driven API.

```go
llmProvider := openai.New(openai.Options{
    BaseURL: "http://127.0.0.1:8080/v1",
    APIKey:  "", // optional; sent as a Bearer token
    Model:   "local-model",
})

rt, err := runtime.New(runtime.Options{
    Workspace: ".",
    Provider:  llmProvider,
    // Resources: optional *resource.Resources
    // Tools:     optional []tool.Tool
    // Security:  optional runtime.Security
})
if err != nil {
    log.Fatal(err)
}
defer rt.Close()

ag, err := rt.NewAgent()
if err != nil {
    log.Fatal(err)
}

home, _ := os.UserHomeDir()
sessions, err := session.NewJSONL(filepath.Join(home, ".iris", "sessions"))
if err != nil {
    log.Fatal(err)
}

c, err := client.New(client.Options{
    Agent:    ag,
    Sessions: sessions,
    SessionDefaults: session.CreateOptions{
        Model:     "local-model",
        Workspace: ".",
    },
})
if err != nil {
    log.Fatal(err)
}

ctx := context.Background()
result, err := c.RunStream(ctx, "Implement the feature", func(event agent.Event) {
    if delta, ok := event.(agent.AssistantDelta); ok {
        fmt.Print(delta.Text)
    }
})
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.SessionID)
```

The lower-level [`agent`](agent) package can be used directly when session management is not needed. `client` adds session-aware runs and JSON-friendly event envelopes; `rpc.NewServer(c).Serve(ctx, in, out)` exposes the same client over JSON-RPC.

### `runtime.Options`

| Field | Default | Purpose |
| --- | --- | --- |
| `Workspace` | process working directory | Canonical workspace root. |
| `Provider` | — (required) | LLM backend implementing `provider.Provider`. |
| `Model` | provider default | Model name forwarded with requests. |
| `Tools` | `nil` | Additional tools registered after the built-ins. |
| `MaxTurns` | 100 | LLM turns per run. |
| `MaxToolCalls` | 200 | Tool executions per run. |
| `ToolTimeout` | 5 minutes | Timeout for one tool execution. |
| `ContextWindow` | 32768 | Context window used by the context manager. |
| `ReserveTokens` | 8192 | Output reservation and request `max_tokens`. |
| `CompactAt` | 0.85 | Compaction trigger fraction. |
| `KeepRecentTokens` | 16384 | Recent tokens kept verbatim. |
| `SummaryMaxTokens` | 2048 | Summary output budget. |
| `Resources` | `nil` | `SYSTEM.md`, `AGENTS.md`, skills, and prompts. |
| `Security` | secure defaults | Filesystem and shell boundary. |
| `Approver` | `nil` | Optional initial approval mechanism. |

`runtime.New` validates the security policy up front, so configuration errors surface before the first run. `Runtime.Warnings()` exposes security warnings, and `Close` closes every agent created by the runtime. `NewAgent` returns `runtime.ErrRuntimeClosed` after `Close`.

### `client` API

`client.Options` fields: `Agent` (required), `Sessions` (nil disables persistence), `SessionDefaults`, and `InitialSession` (resume an existing `*session.Session`).

`client.Client` provides:

| Method | Purpose |
| --- | --- |
| `Run(ctx, prompt)` | Run one prompt and return the final `Result`. |
| `RunStream(ctx, prompt, handler)` | Run one prompt and forward typed events to `handler`. |
| `Cancel()` | Abort the active run. |
| `Abort()` | Abort the active run and return instructions that were queued but not delivered. |
| `Steer(prompt)` | Queue a steering instruction for the active run. |
| `FollowUp(prompt)` | Queue a follow-up for when the active run would stop. |
| `ClearQueue()` | Remove and return queued instructions. |
| `Pending()` | Snapshot queued instructions. |
| `SessionID()` | Current session ID, if any. |
| `IsRunning()` | Whether a run is active. |
| `NewSession()` | Persist the current session and start an empty one. |
| `ResumeSession(id)` | Load a persisted session by ID. |
| `ResumeLatest()` | Load the most recently updated session. |
| `ForkSession(entryID)` | Fork from a specific user entry. |
| `ForkSessionAt(index)` | Fork from a 1-based user-message index. |
| `ListSessions()` | Session summaries, newest first. |
| `ExportSession()` | Persist and export the current session as Markdown. |

`client.Result`:

| Field | Meaning |
| --- | --- |
| `Content` | Final assistant text of the last turn (falls back to the last assistant message in history). |
| `Reasoning` | Final reasoning text, when the model produced one. |
| `SessionID` | Active session ID. |
| `Usage` | Token usage summed across turns in the run. |
| `ContextSummaryUsage` | Token usage spent on compaction summaries, when any. |
| `Canceled` | Whether the run ended because it was cancelled. |

`client.ErrBusy` is returned when a run is requested while another is active. Mutating session operations are also busy-checked. `RunStream` handlers run synchronously in the agent run goroutine and must return quickly. `client.EventRecord{Type, Event}` and `client.Record(event)` provide the stable JSON envelope used by JSONL and RPC.

### Providers

Providers stream typed events:

```go
type Provider interface {
    Stream(ctx context.Context, request Request) (<-chan StreamEvent, error)
}

type StreamEvent interface { streamEvent() }

type DeltaEvent struct { Delta Delta }
type DoneEvent struct { Response Response }
type ErrorEvent struct { Err error }
```

A stream contains zero or more `DeltaEvent`s followed by exactly one `DoneEvent` or `ErrorEvent`. Setup failures are returned directly from `Stream`; failures after the stream starts arrive as `ErrorEvent`. The channel must be closed after the terminal event, and context cancellation must stop the request. The `StreamEvent` interface has an unexported method, so external packages use the exported event types rather than defining new ones.

OpenAI-compatible Chat Completions is the first implementation, available through [`provider/openai`](provider/openai). `provider.Collect` consumes a stream and returns the final response; it reports an error if the channel closes without a terminal event. `provider.ProviderFunc` adapts a stream function; `provider.ResponseFunc` adapts a non-streaming function and emits a single `DoneEvent`.

`openai.New(openai.Options{...})` accepts:

| Option | Default | Purpose |
| --- | --- | --- |
| `BaseURL` | `http://127.0.0.1:8080/v1` | API root; `/chat/completions` is appended. |
| `APIKey` | `""` | Bearer token, when set. |
| `Model` | `""` | Default model name. |
| `MaxRetries` | `3` | HTTP attempts (minimum 1). |
| `RequestTimeout` | 30 minutes | Deadline for one complete request. |
| `HTTPClient` | no timeout | Optional custom HTTP client. |

The transport sets `stream=true` and requests usage in the stream. Non-transient 4xx responses are not retried (429 is retried). A 413 response or an error that looks like a context-window overflow is wrapped in `provider.ErrContextWindow`.

Model capabilities are descriptive for now:

```toml
[[models]]
name = 'local-model'
context_size = 75000
tool_calling = true
reasoning = true
structured_output = false
vision = false
sampling_parameters = ['temperature', 'top_p', 'top_k']
```

The corresponding Go type is `provider.Capabilities`. Zero values mean the capability was not declared; providers must not assume support. `context_size` and the sampling values do affect requests, while the capability flags and `sampling_parameters` are metadata.

### Public API

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

`History` returns a snapshot including the system message. `LoadHistory` replaces the conversation while the agent is idle and prepends the system prompt when the messages do not start with one. `Subscribe` has no unsubscribe; the subscription lives for the agent's lifetime. `ApprovalTimeout` reports the policy deadline and `Approver` returns the installed approver, including one configured through an approval hook. `Close` cancels the active run, waits for cleanup, and releases captured temp files. A single run is active at a time; starting another run returns `agent.ErrAgentBusy`, and using an agent after `Close` returns `agent.ErrAgentClosed`.

While a run is active, `Steer` and `FollowUp` queue instructions for the next turn and for the stop point respectively. `Pending` snapshots the queues and `ClearQueue` removes and returns them. `Cancel` aborts the run; `Abort` also closes the queue atomically and returns the instructions that had not been delivered, so a UI can restore their text. Queue methods are safe to call from event handlers and from other goroutines, and they never interrupt an in-flight provider request or tool execution. See [Execution Control](#execution-control) for the exact delivery points.

| Event | Meaning |
| --- | --- |
| `agent.started` | A run began; carries the input. |
| `agent.finished` | A run ended, including cancellation or error status. |
| `agent.turn.started` | A new LLM turn began; flush buffered deltas. |
| `assistant.delta` | Assistant text increment. |
| `reasoning.delta` | Reasoning text increment. |
| `tool.started` | A tool is about to execute. |
| `tool.progress` | Streaming tool arguments or execution progress. |
| `tool.completed` | A tool finished successfully. |
| `tool.failed` | A tool failed, timed out, or was skipped. |
| `usage` | Token accounting for an LLM call. |
| `context.compaction.started` | Iris began summarizing or trimming older context. |
| `context.compaction.finished` | The compaction pass ended, with before/after estimates and counts. |
| `agent.queue.updated` | The pending steering/follow-up queue after a delivery or a discard at run end. |
| `agent.instruction.delivered` | A queued instruction became a user message; carries its kind and text. |
| `agent.error` | A run-level error. |

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
    case agent.AgentError:
        fmt.Fprintln(os.Stderr, e.Message)
    case agent.AgentFinished:
        fmt.Println("done, canceled:", e.Canceled)
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
| [`agent`](agent) | Agent interface, execution control (steer/follow-up/abort), typed events, approvals, errors. |
| [`provider`](provider) | Streaming LLM provider interface, stream events, capabilities, provider-neutral request/response types. |
| [`tool`](tool) | Tool and registry interfaces, plus `tool.Environment` for custom tools. |
| [`runtime`](runtime) | Constructor, runtime/security options, provider/tool composition root. |
| [`session`](session) | Session model and JSONL manager. |
| [`resource`](resource) | Global/project Markdown resource discovery and precedence. |
| [`client`](client) | Session-aware headless API and event envelope. |
| [`rpc`](rpc) | JSON-RPC 2.0 server for remote control. |
| [`provider/openai`](provider/openai) | OpenAI-compatible provider transport. |

Implementation details stay under `internal/`.

### Errors

| Sentinel | Meaning |
| --- | --- |
| `agent.ErrAgentBusy` | A run is already active. |
| `agent.ErrAgentClosed` | The agent was closed. |
| `agent.ErrAgentIdle` | A steering instruction or follow-up was submitted while no run was active. |
| `agent.ErrEmptyInput` | A steering instruction or follow-up contained only whitespace. |
| `agent.ErrTurnBudgetExceeded` | `max_turns` was exhausted. |
| `agent.ErrToolBudgetExceeded` | `max_tool_calls` was exhausted. |
| `agent.ErrToolTimeout` | Wraps a tool execution that exceeded its timeout (reported as a failed tool). |
| `client.ErrBusy` | A client run or mutating session operation was attempted while busy. |
| `runtime.ErrRuntimeClosed` | `NewAgent` was called after `Runtime.Close`. |
| `session.ErrSessionNotFound` | The session ID does not exist. |
| `session.ErrNoSessions` | No persisted sessions exist. |
| `session.ErrInvalidSessionID` | The session ID is unsafe or malformed. |
| `session.ErrInvalidForkPoint` | The fork target is missing or not a user message. |
| `provider.ErrContextWindow` | The provider rejected a prompt for exceeding the context window. |

Use `errors.Is` with these sentinels. `provider.IsContextWindowError` also recognizes common provider error text.

## Development

```bash
❯ make help
lint      - Run linters (goimports, go vet, golangci-lint)
build     - Clean, compile and install
test      - Run all tests (with race detection)
coverage  - Run tests with coverage report (to ./output/coverage.out)
```

| Command | Effect |
| --- | --- |
| `make` | Default target: `lint` then `build`. |
| `make all` | `lint`, `build`, `test`, `coverage`. |
| `make build` | Clean, tidy modules, build `./bin/iris` with stripped/trimmed flags, and `go install`. |
| `make lint` | `goimports -w .`, `go vet ./...`, `golangci-lint run`. |
| `make test` | `go test -race -short ./...`. |
| `make coverage` | Coverage profile at `./output/coverage.out` (no race detector). |
| `make help` | Print the documented targets. |

`make lint` requires `goimports` and `golangci-lint` on `PATH`.

CI (`.github/workflows/go.yml`) runs `go build -v ./...` and `go test -v ./...` on pushes and pull requests to `main`, using the Go version from `go.mod`.

## My Setup

I'm currently running Iris on a Mac mini M4 with 16GB RAM. I use llama.cpp to serve the following models (context size 75k):

- [bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M](https://huggingface.co/bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF)
- [unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_M](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) - _reasoning is disabled_
- [unsloth/gemma-4-12B-it-GGUF:Q4_K_M](https://huggingface.co/unsloth/gemma-4-12B-it-GGUF)

## Why Iris Exists

Most modern AI systems assume large cloud models, massive context windows, and API dependency. Iris explores a different direction:

> What if a strong agent harness makes it possible to run small open models locally, and still achieve useful results?

## Similar Projects

The only working one I know is [Pi agent harness](https://pi.dev/)

## License

Iris is licensed under the [MIT License](LICENSE).
