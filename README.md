# Iris

**Iris** is a local-first AI agent built in Golang, designed to assist with software development and evolve into a general-purpose autonomous agent. It is optimized specifically for small open models (~9B parameters) running on local hardware.

## How It Works

Iris runs a **deterministic agentic loop** with three states:

1. **`idle`** → Sends a request to the LLM to generate the next response
2. **`tool`** → Executes any tool calls returned by the LLM (read, edit, write, shell)
3. **`done`** → The LLM has finished and loop ends

The agent maintains a conversation history and can make multi-step decisions, calling tools iteratively until the task is complete.

## Built-in Tools

| Tool | Description |
| --- | --- |
| `read` | Read file contents (with line range support), confined to the workspace |
| `edit` | Edit files via unique, non-overlapping exact replacements (atomic write) |
| `write` | Atomically write/create files (creates parent directories), confined to the workspace |
| `shell` | Execute policy-checked shell commands with approval, timeouts, and bounded output |

## Security and Permissions

Iris deliberately does not ship a full permission UI framework. It provides a
small, explicit execution boundary, a one-line approval prompt for the TUI, and
extension points, in the spirit of Pi's container-first approach.

### File sandbox

Every file tool resolves the requested path, follows symlinks, and checks the
result against the configured roots. `..` escapes, symlinks that point outside
the workspace, dangling symlinks, and non-directory path components are
rejected. The default mode is `workspace-write`:

| `security.mode` | Reads | Writes | Shell |
| --- | --- | --- | --- |
| `read-only` | inside workspace | denied | denied |
| `workspace-write` (default) | inside workspace | inside workspace | per shell policy |
| `danger-full-access` | anywhere | anywhere | per shell policy |

Extra roots can be granted with `security.read_paths` and
`security.write_paths`. The workspace itself is the `--workdir` directory
(or the current directory).

The read tool is also granted read-only access to Iris' own temp directory so
it can inspect full shell output. Nothing else in that directory is writable by
the file tools.

### Shell policy

`security.shell.mode` controls command execution:

| Mode | Behaviour |
| --- | --- |
| `deny` | The shell tool is disabled. |
| `allowlist` | Only commands matching `security.shell.allow` run; everything else is denied. |
| `approval` (default) | Allowlisted commands run directly. Everything else requires an approval decision. |
| `allow` | Every command that does not match a deny rule runs. Requires `security.external_sandbox = true`. |

Deny rules are evaluated first and cannot be overridden by approval. A small
built-in deny list (privilege escalation, disk formatting, root deletion,
fork bombs) is applied by default and can be removed with
`security.shell.disable_default_deny = true`. Allow and deny patterns are globs:
patterns with whitespace match the whole command or one command segment,
patterns without whitespace match executable names. Commands containing
command substitution, subshells, heredocs, shell wrappers (`bash -c`, `eval`,
`env`, `xargs`, …), or interpreters are treated as opaque and are never
allowlisted. They must go through approval.

Allowlisting a build tool, interpreter, or test runner is equivalent to
allowing it to execute project code; tools such as `git` can also run hooks or
aliases. Keep the allowlist narrow and read-only where possible.

Approval can be provided in two ways:

1. **Interactive TUI** (default when no hook is configured): a one-line
   `[y] allow once / [n] deny` prompt is shown above the input box.
2. **Approval hook**: set `security.shell.approval_hook` to an argv list. Iris
   sends `{kind, command, workdir, programs, reason}` as JSON on stdin and
   treats exit status `0` as allow, any non-zero as deny, with stdout/stderr
   used as the denial reason.

Go embedders can implement `policy.Approver` (or use `policy.ApproverFunc`)
and install it with `Policy.SetApprover`.

If no approval mechanism is available (for example in a non-interactive
embedding) commands that need approval fail closed. Approval requests are
bounded by `approval_timeout_seconds` (default 5 minutes).

### Timeouts, output, and temp files

- Shell commands run in their own process group and are killed (including
  children) when `timeout_seconds` expires or the context is cancelled.
- Output returned to the model is bounded to the last
  `max_output_bytes` / `max_output_lines` (default 25KB / 1000 lines).
- Up to `max_capture_bytes` (default 10MB) of full output is written to a
  `0600` file in a private session directory. The path is returned only when
  the model-visible output was truncated, and the read tool can read it.
- Session directories are removed on exit. Directories left by crashed
  processes are reaped after `temp_file_ttl_minutes` (default 24 hours).

### Cancellation and execution budgets

Each agent run has its own `context.Context`. Cancellation propagates to the
LLM HTTP request, stream reading, retry backoff, approval requests, and tool
execution. Interrupting a tool call records synthetic tool results for the
remaining calls so the conversation history stays valid for the next run.

- `[llm] request_timeout_seconds` bounds a complete LLM request, including
  retries and streaming (default 30 minutes). Retry backoff is
  cancellation-aware, and non-transient 4xx responses are not retried.
- `[agent] max_turns` bounds LLM turns per run (default 100).
- `[agent] max_tool_calls` bounds tool executions per run (default 200).
- `[agent] tool_timeout_seconds` bounds a single tool execution (default
  5 minutes).
- Exhausting a budget returns a clear error instead of looping indefinitely.

Press `Ctrl+C` while Iris is streaming to cancel the current run; press it
again to quit. `SIGTERM` exits the TUI, cancels the active run, and waits for
tool and temp-file cleanup before the process exits.

### Edits are validated before they are applied

The edit tool requires every `find` string to occur exactly once in the
original file and rejects overlapping edits. Files are written atomically via a
temp file and rename, preserving permissions. Failed validation leaves the file
untouched.

### External sandbox requirement

The shell policy is a gate, not an OS sandbox. A permitted shell command can
read and write anything the Iris process can, including paths outside the
workspace. The file sandbox does not constrain shell commands. For a hard
boundary, run Iris inside a container or VM and set:

```toml
[security]
external_sandbox = true
```

Iris prints a warning at startup and tells the model about this limitation when
`external_sandbox` is not acknowledged. `security.shell.mode = 'allow'` is
refused unless the external sandbox is acknowledged.

The path checks are resolved before use (including symlinks), but they are not
a defence against a hostile process racing Iris between the check and the
write. Treat the workspace as trusted input and use an external sandbox for
untrusted repositories.

## Requirements

- **Go 1.26.0+**
- **LLM server**: Any OpenAI-compatible API endpoint (e.g. Ollama, llama.cpp, vLLM)
- **Terminal**: TUI requires a terminal emulator (e.g. iTerm2, Alacritty, Ghostty)

## Quick Start

### 1. Build

```bash
make build
```

### 2. Start an LLM server

For example, with llama.cpp:

```bash
llama serve
```

### 3. Configure Iris

Iris auto-creates a `~/.iris/settings.toml` on first run. Edit it to set your LLM. Here is a sample configuration:

```toml
base_url = 'http://127.0.0.1:8080/v1'
max_retries = 3
default_model = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'

[[models]]
name = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 0.6
top_p = 0.95
top_k = 20

[[models]]
name = 'unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 1.5

[[models]]
name = 'unsloth/gemma-4-12B-it-GGUF:Q4_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 64

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
./bin/iris --debug --workdir ./my-project --base-url http://127.0.0.1:11434/v1 --model model-name
```

## CLI Options

```bash
❯ ./bin/iris --help
iris is an AI agent that can perform various tasks based on user input.

Usage:
  iris [flags]

Flags:
  -b, --base-url string   Set the LLM API URL (Defaults to http://127.0.0.1:8080/v1)
  -d, --debug             Enable debug logging
  -h, --help              help for iris
  -m, --model string      Set the LLM model name (e.g. unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_XL)
  -w, --workdir string    Set the working directory for the agent (Defaults to current directory)
```

## TUI Key Bindings

| Binding | Action |
| --- | --- |
| `Enter` | Send message |
| `Shift+Enter` or `Ctrl+J` | Insert newline |
| `PgUp` / `PgDn` or `Ctrl+U` / `Ctrl+D` | Scroll history |
| `Ctrl+C` | Cancel the current run; press again (or press while idle) to quit |

## Public API

The agent runtime has no dependency on the TUI. Consumers subscribe to a typed
event stream and drive runs with a context:

```go
type Agent interface {
    Run(ctx context.Context, input string) error
    Subscribe(handler func(Event))
    Cancel()
    Close() error
    SetApprover(Approver)
    Approver() Approver
    ApprovalTimeout() time.Duration
}

type Event interface {
    Type() EventType
}
```

| Event | Meaning |
| --- | --- |
| `agent.started` | A run began. |
| `agent.finished` | A run ended, including cancellation or error status. |
| `agent.turn.started` | A new LLM turn began; flush buffered deltas. |
| `assistant.delta` | Assistant text increment. |
| `reasoning.delta` | Reasoning text increment. |
| `tool.started` | A tool is about to execute. |
| `tool.progress` | Streaming tool arguments or execution progress. |
| `tool.completed` | A tool finished successfully. |
| `tool.failed` | A tool failed, timed out, or was skipped. |
| `usage` | Token accounting for an LLM call. |
| `agent.error` | A run-level error. |

```go
ag.Subscribe(func(event agent.Event) {
    switch e := event.(type) {
    case agent.AssistantDelta:
        fmt.Print(e.Text)
    case agent.ToolStarted:
        fmt.Println("tool:", e.ToolName)
    case agent.AgentError:
        fmt.Fprintln(os.Stderr, e.Message)
    case agent.AgentFinished:
        fmt.Println("done, canceled:", e.Canceled)
    }
})

if err := ag.Run(ctx, "Implement the feature"); err != nil {
    // ctx cancellation, a budget, or a run error
}
```

Event handlers run synchronously on the run goroutine; keep them fast and do
not call `Run` or `Close` from inside a handler. The TUI is just one consumer of
this stream.

Stable public packages:

- [`agent`](agent) — agent interface, typed events, approval hooks, errors.
- [`provider`](provider) — LLM provider interface and provider-neutral request,
  response, message, tool, delta, and usage types.
- [`tool`](tool) — tool and registry interfaces.
- [`runtime`](runtime) — constructor, runtime/security options, and the
  provider/tool composition root. Implementation details stay under `internal/`.

```go
rt, err := runtime.New(runtime.Options{
    Workspace: ".",
    Provider:  myProvider,
    Tools:     []tool.Tool{NewMyTool()},
})
if err != nil {
    log.Fatal(err)
}
defer rt.Close()

ag, err := rt.NewAgent()
if err != nil {
    log.Fatal(err)
}
```

## Making Custom Tools

Implement the public `tool.Tool` interface and register the tool through
`runtime.Options.Tools`:

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
        _ = path
        _ = err
    }
    return map[string]any{"result": "done"}, nil
}
```

## Development

```bash
❯ make
build - Clean, compile and install
lint - Run linters (goimports, go vet, golangci-lint)
test - Run all tests (with race detection)
coverage - Run tests with coverage report (to ./coverage.out)
```

## My Setup

I'm currently running Iris on a Mac mini M4 with 16GB RAM. I use llama.cpp to serve the following models (context size 75k):

- [bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M](https://huggingface.co/bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF)
- [unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_M](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) - _reasoning is disabled_
- [unsloth/gemma-4-12B-it-GGUF:Q4_K_M](https://huggingface.co/unsloth/gemma-4-12B-it-GGUF)

## Why Iris Exists

Most modern AI systems assume large cloud models, massive context windows, and API dependency. Iris explores a different direction:

> What if strong agent harness makes it possible to run small open models locally, and still achieve useful results?

## Similar Projects

The only working one I know is [Pi agent harness](https://pi.dev/)

## License

Iris is licensed under the [MIT License](LICENSE).
