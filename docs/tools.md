# Tools

[← Documentation index](README.md)

---

## Built-in tools

| Tool | Required args | Optional args | Result keys |
| --- | --- | --- | --- |
| `read` | `path` | `start`, `max` | `content`, `truncated`, `start_line`, `lines`, `end_line`, `path`, plus `next_start` and `truncation_notice` when truncated |
| `edit` | `path`, `edits` | — | `success`, `edits`, `changed` |
| `write` | `path`, `content` | — | `success`, `bytes` |
| `shell` | `command` | — | `output`, `exit_code`, `success`, `truncated`, `duration_ms`, plus `timed_out`, `canceled`, `error`, `full_output_file`, `full_output_truncated`, `full_output_bytes`, `truncation_notice`, and line/byte metadata when relevant |

Tools other than the paging `read` tool can additionally receive the common output fields described in [Tool output management](#tool-output-management).

Before a tool runs, Iris checks the call against the tool's top-level `Parameters` schema (required keys and basic types). Invalid JSON, truncated arguments, missing required values, and unknown tool names are reported to the model as `{"ok":false,"error":{...}}` tool results and are never executed. A tool that runs but fails or times out keeps its failure in conversation history; it is not retried automatically. See [Structured tool errors](execution.md#structured-tool-errors).

## `read`

Reads a file inside the workspace. `start` is 1-based; `max` defaults to 1000 lines and is capped at 10,000. The result is also capped at 25 KiB. Use `start`/`max` to page through larger files. When truncated, the result includes `path`, `next_start`, and a `truncation_notice` so the model can continue; the source file remains the full output. Directories, special files, and paths outside the allowed roots are rejected.

## `edit`

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

## `write`

Writes or creates a file inside the workspace, creating parent directories as needed. Existing files keep their permissions; new files are created with `0644`. Writes are atomic.

## `shell`

Runs `<security.shell.binary> -c <command>` in the workspace, subject to the shell policy. The default shell is `bash` when available, otherwise `sh`; override it with `security.shell.binary`. Shell commands are not confined by the file sandbox.

## Tool output management

Tool results are post-processed by the same output policy before they are sent to the model; the paging `read` tool is the one deliberate exception because it is the retrieval path for omitted output. The policy is tuned with the existing `[security.shell]` limits, which are shared by all tools: `max_output_bytes` (default 25 KiB), `max_output_lines` (default 1000), and `max_capture_bytes` (default 10 MiB).

The model-visible result is bounded:

- A large text value is replaced by a head-and-tail preview. Shell keeps the last lines so late failures and compiler errors survive; other tools keep the beginning and the end.
- The truncated result contains `truncated: true`, a human-readable `truncation_notice`, and an omission marker at the cut point.
- Metadata includes total/shown/omitted lines and bytes, plus `full_output_file`, `full_output_bytes`, and `full_output_truncated` when a full file exists.
- If a structured result is too large even without one oversized text value (for example, a huge array), Iris stores the pretty-printed result separately and returns a bounded preview with the same notice fields.

Example notice:

```text
Output truncated.
Showing lines 1-200 and 4383-4582 of 4582.
4182 lines omitted (123,456 bytes).
Full output:
/Users/me/.iris/sessions/tool-output/large-abc123.log
```

The full output is stored separately from the model-visible transcript:

- Session-backed agents (client, print, JSONL, RPC, TUI) store it under `<session-dir>/tool-output/<tool>-<random>.log`, with directory `0700` and files `0600`. The directory is shared by sessions in that store so a forked or resumed transcript's paths remain readable; it is not listed as a session.
- Standalone agents, or custom session managers that do not implement `session.ToolOutputProvider`, use the agent's private ephemeral tool-output directory, which is removed on `Close`.
- Shell writes the full raw stdout/stderr (up to `max_capture_bytes`) and returns a tail preview plus line metadata. Other tools store the full primary text; oversized structured results store the pretty-printed JSON up to the capture limit.
- When the stored full output itself exceeds `max_capture_bytes`, `full_output_truncated` is true and the notice says so.

The model can retrieve omitted output with the `read` tool:

```json
{"path": "/Users/me/.iris/sessions/tool-output/large-abc123.log", "start": 4383, "max": 800}
```

The `read` result itself is paged: when it is truncated, it includes `next_start` and a `truncation_notice` so the model can continue. It has its own 25 KiB/1000-line page cap (up to 10,000 lines when requested) and is not further reduced by the common tool-output cap, because it is the retrieval path for omitted output. Full output is never inserted into the model context automatically; only the bounded preview and the path are.

Tool output files can contain sensitive material from commands and files. They are private (`0700`/`0600`) but persist with session output; remove old session stores if the data should not remain on disk.

## Custom tools

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
