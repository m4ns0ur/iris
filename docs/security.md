# Security and Permissions

[← Documentation index](README.md)

---


Iris deliberately does not ship a full permission UI framework. It provides a small, explicit execution boundary, a one-line approval prompt for the TUI, and Go extension points such as custom tools and approvers. There is no project extension or plugin loader; project resources are data, not code.

## File sandbox

In `read-only` and `workspace-write` modes, every file tool resolves the requested path, follows symlinks, and checks the result against the configured roots. `..` escapes, symlinks that point outside the workspace, dangling symlinks, symlink loops, non-directory path components, NUL bytes, and special files are rejected. The default mode is `workspace-write`:

| `security.mode` | Reads | Writes | Shell |
| --- | --- | --- | --- |
| `read-only` | inside workspace | denied | denied |
| `workspace-write` (default) | inside workspace | inside workspace | per shell policy |
| `danger-full-access` | anywhere | anywhere | per shell policy |

`read-only` denies all writes and disables the shell tool, regardless of `security.shell.mode`.

Extra roots can be granted with `security.read_paths` and `security.write_paths`. Write roots are also readable. The workspace itself is the `--workdir` directory (or the current directory) and is canonicalized at startup.

In `danger-full-access` mode relative paths are still resolved against the workspace, but paths are not canonicalized or confined. Use it only when the process is already isolated.

The read tool is also granted read-only access to Iris' own tool-output directory so it can inspect full tool output (the session store directory when a session is attached, ephemeral otherwise). No separate write grant is added for that directory; if it lies inside a writable workspace root, normal workspace permissions still apply.

Path checks are resolved before use (including symlinks), but they are not a defence against a hostile process racing Iris between the check and the write. Treat repository files, project instructions, shell output, and tool results as untrusted input; policy still denies or asks for risky operations, and an external sandbox remains the right boundary for hostile repositories.

Instruction trust is a separate boundary from the file sandbox. Project `AGENTS.md`, project `.iris/SYSTEM.md`, project skills/prompts, repository files, shell output, and tool results are untrusted data. They cannot change this execution policy. See [Project trust and instruction provenance](resources.md#project-trust-and-instruction-provenance).

## Shell policy

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

## Approvals

Approval can be provided in three ways:

1. **Interactive TUI** (default when no approver is configured): a one-line
   `[y] allow once · [n] deny` bar is shown above the input box. `y`/`Y` allows, `n`/`N`/`Esc` denies, and `Ctrl+C` denies and cancels the run.
2. **Approval hook**: set `security.shell.approval_hook` to an argv list. Iris sends `{kind, command, workdir, programs, reason}` as JSON on stdin and treats exit status `0` as allow, any non-zero as deny. Stdout/stderr are used as the denial reason and are capped at 64 KiB. The hook runs directly (never through a shell), inherits `IRIS_APPROVAL_KIND` and `IRIS_WORKDIR`, and is bounded by `approval_timeout_seconds`.
3. **Go embedders**: implement `agent.Approver` (or use `agent.ApproverFunc`) and install it with `Agent.SetApprover` or `runtime.Options.Approver`.

If no approval mechanism is available (for example in a non-interactive embedding), commands that need approval fail closed. Approval requests are bounded by `approval_timeout_seconds` (default 5 minutes). The TUI mirrors that deadline so a timed-out prompt disappears instead of blocking the next turn.

## Timeouts, output, and temp files

- Shell commands run in their own process group on Unix and are killed (including children) when `timeout_seconds` expires or the context is cancelled. On other platforms only the direct child is killed.
- A non-zero exit code is not a tool error: the result always contains `output`, `exit_code`, `success`, `truncated`, and `duration_ms`; `timed_out`, `canceled`, and `error` appear when relevant.
- Shell output returned to the model is bounded by the common tool-output policy: the last `max_output_bytes` / `max_output_lines` (default 25 KiB / 1000 lines), so failures at the end of `go test` output survive. A truncation notice includes the shown line range, omitted bytes/lines, and the full-output path.
- Up to `max_capture_bytes` (default 10 MiB) of full raw output is stored in a `0600` file. Session-backed agents use `<session-dir>/tool-output/`; standalone agents use a private ephemeral directory. The read tool can read the path.
- Ephemeral tool-output directories are created under the user cache directory (`.../iris/tmp/session-*`, falling back to the system temp directory) with `0700` permissions and are removed on exit. Directories left by crashed processes are reaped after `temp_file_ttl_minutes` (default 24 hours). The session-store tool-output directory persists when the agent closes; it is not removed with the session process. See [Tool output management](tools.md#tool-output-management).

## Cancellation and execution budgets

Each agent run has its own `context.Context`. Cancellation propagates to the LLM HTTP request, stream reading, retry backoff, approval requests, and tool execution. Interrupting a tool call records synthetic tool results for the remaining calls so the conversation history stays valid for the next run.

- `[llm] request_timeout_seconds` bounds a complete LLM request, including retries and streaming (default 30 minutes). Retry backoff is cancellation-aware, and non-transient 4xx responses are not retried.
- `[agent] max_turns` bounds LLM turns per run (default 100). Delivered steering instructions and follow-ups count as turns.
- `[agent] max_tool_calls` bounds tool executions per run (default 200).
- `[agent] tool_timeout_seconds` bounds a single tool execution (default 5 minutes).
- Exhausting a turn or tool budget returns a clear error instead of looping indefinitely. A tool timeout is reported as a failed tool result and the run continues.
- Tool panics are recovered and converted into tool errors so a buggy tool cannot crash the agent.
- Tool calls are never retried automatically. Only model requests use transport retries, and only before a response stream is consumed. Repeating a file write or shell command could duplicate side effects; a failed tool result is returned to the model instead.

Press `Ctrl+C` while Iris is streaming to abort the current run; queued instructions are restored to the input, and pressing `Ctrl+C` again quits. `SIGTERM` exits the TUI, cancels the active run, and waits for tool cleanup before the process exits. `Agent.Close` cancels, waits for cleanup, and removes ephemeral tool-output files; session tool-output files persist. Steering and follow-up are separate from cancellation and are described in [Execution Control](execution.md#execution-control).

## Edits are validated before they are applied

The edit tool requires every `find` string to occur exactly once in the original file and rejects overlapping edits. Files are written atomically via a temp file and rename, preserving permissions. The path is re-resolved immediately before writing; if it changed (for example a symlink was swapped), the edit is refused. Failed validation leaves the file untouched.

## External sandbox requirement

The shell policy is a gate, not an OS sandbox. A permitted shell command can read and write anything the Iris process can, including paths outside the workspace. The file sandbox does not constrain shell commands. For a hard boundary, run Iris inside a container or VM and set:

```toml
[security]
external_sandbox = true
```

Iris prints warnings at startup and tells the model about this limitation when `external_sandbox` is not acknowledged. `security.shell.mode = 'allow'` is refused unless the external sandbox is acknowledged (a read-only file mode disables the shell regardless).

Iris also warns about `danger-full-access`, `security.shell.mode = 'allow'`, and `disable_default_deny = true`. Warnings are written to the debug log and stderr at startup, and the policy summary is included in the system prompt.

## Logging and sensitive data

Iris writes `iris_debug.log` in the process working directory (not `--workdir`), in append mode with `0600` permissions. The log is JSON lines. `--debug` enables debug-level logging; otherwise only info and above are written. Debug logs include LLM payload previews and tool output, so treat the file as sensitive and avoid sharing it.
