# TUI

[← Documentation index](README.md)

---


The TUI is one consumer of the agent event stream. On launch it creates a new session, shows its ID, displays resource warnings, and wires an interactive approver when no approver is configured.

## Key bindings

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

The input box stays editable while Iris is streaming. `Enter` queues a steering instruction (delivered after the current assistant turn finishes its tool calls, before the next LLM request) and `Alt+Enter` queues a follow-up (delivered only when the run would otherwise stop). Queued instructions are shown in the status bar and as `You (steer)>` / `You (follow-up)>` messages when delivered. Command completion and delivery semantics are described in [Execution Control](execution.md#execution-control).

The status line shows the current hints, the scroll percentage, and the total tokens reported by the latest LLM call. While streaming it shows the active phase (`thinking`, `reasoning`, `tooling`, `compacting`, `canceling`) and any queued instruction counts; when the history is scrolled up it shows a scroll hint. The TUI discards partial output when a turn is retried after context compaction, and shows notices for truncated responses and empty model responses.

## Commands

| Command | Purpose |
| --- | --- |
| `/new` | Create a new session. |
| `/resume [id]` | Resume the latest session or one matching an ID prefix. |
| `/fork [n]` | Fork from the nth user message (1-based); omit `n` for the latest user message. |
| `/export` | Export the current session as Markdown under the session directory. |
| `/clear-queue` | Discard queued steering/follow-up instructions and restore their text to the input. Available while streaming. |
| `/skills` | List discovered skills with their trust level. |
| `/skill <name>` | Insert a skill into the input box. Project skills are wrapped as untrusted content. |
| `/prompts` | List prompt templates with their trust level. |
| `/prompt <name> [input]` | Render a template into the input box. Project templates are wrapped as untrusted content. |
| `/help`, `/?` | List commands. |

Unknown commands produce a notice. Session-mutating commands persist the current session first and are unavailable while a run is active; `/clear-queue` and `/help` remain available. The TUI uses the same durable recorder as the headless modes: user messages, assistant tool calls, and tool results are on disk while the run is still in progress. Recovered sessions show notices for interrupted tool calls. After each settled run the TUI appends the execution summary described in [Execution Summaries](execution.md#execution-summaries).
