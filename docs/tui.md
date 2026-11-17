# TUI

[← Documentation index](README.md)

---

The TUI is one consumer of the agent event stream. On launch it creates a new session, shows a compact session header, displays resource warnings, and wires an interactive approver when no approver is configured.

The interface is intentionally quiet. Routine activity is one line, details are available on demand, and only approvals, errors, and help use more vertical space.

## Layout

The frame is a transcript viewport, a single rule, the prompt, and a one-line status footer:

```
  · transcript (scrollable)

──────────────────────────────────────────────────────────
❯ Ask Iris…
  enter send · /help                    Qwen3.5-9B · repo · ctx 18.4k/75k · total 7,312
```

The transcript opens with the session header, Iris's mark beside the model, session, workspace, and policy lines:

```
 ▄▀▀▀▄   iris · qwen3.6-35B-Q1 · session s-6c6dd1
▐░▓█▓░▌  /tmp/foo · workspace-write sandbox · shell approval
 ▀▄▄▄▀
```

The mark is three rows of block art in the brand color, sized so every blink frame has the same shape and the header text never moves. It blinks once on launch: a short pause so the open mark is visible, then open, half, shut, half, open at about 80 ms per frame, and then it stays open. The sequence is one shot, so no timer keeps running while the TUI is idle. On terminals narrower than about 33 columns the header falls back to the single-cell `◉` and two lines.

The status footer shows the active model, workspace basename, context usage, and the cumulative token total for the session. The left side is state-aware: it shows an animated phase indicator and elapsed time, the queued instruction count, a scroll hint, or the relevant key hint. The phase word is omitted; the spinner family identifies the phase, except while the server has not answered at all:

| Indicator | Phase |
| --- | --- |
| `◐ ◓ ◑ ◒` half-circle rotation | thinking |
| `◴ ◷ ◶ ◵` quarter-circle rotation | reasoning |
| `◉ ◎ ◍ ◌ ○` ring pulse | responding |
| `◜ ◝ ◞ ◟` arc rotation | tooling |
| `⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏` braille dots | waiting for the server |
| `☱ ☲ ☴` trigram rotation | compacting context |
| fading blocks | canceling |

Color reinforces the distinction: reasoning and compaction are yellow, tooling is purple, canceling is red, and thinking/responding are orange. Context usage turns yellow near the compaction threshold and red near the limit. The footer never wraps or grows.

The waiting phase exists because a dispatched request can produce no bytes for a long time: the server may be connecting, queueing the job, loading a model on demand, or pre-filling a long prompt. The runtime emits a request event when the request is sent and a response event when the server accepts it, so the footer only shows the waiting dots in the first, genuinely silent case. After a second of silence the left side names the wait (`waiting for server 4s`) in place of the run timer, so the same seconds are never printed twice; after ten seconds the hint adds the likely cause (`· model may be loading`) and the spinner turns yellow. A long silence after the server answered is ordinary thinking or prefill and keeps the thinking indicators. `/status` reports the current wait as well.

The `ctx` figure is the current context size and window (`1.6k/65.5k`), not a session total: it is the provider-reported prompt size of the latest request plus that request's completion, so it grows with the conversation that is actually resent. The `total` figure is the cumulative tokens consumed in the session. Every request resends the whole prompt, so `total` normally runs several times larger than the window even while the live context stays well below it; compaction is driven by the context figure, never by the total. The execution summary after each run reports that run's usage; small runs show exact counts (for example `1,412 tok`) so two short replies do not look identical after rounding.

The input grows upward, up to roughly one third of the terminal (hard limit: 14 lines). The transcript keeps at least five visible rows. When the input is capped, the rule shows `↑ N` indicating lines hidden above; the prompt scrolls internally with the cursor.

## Key bindings

| Binding | Action |
| --- | --- |
| `Enter` | Send the prompt, or run the highlighted slash command (inserted instead when it needs an argument) |
| `Enter` (streaming) | Queue a steering instruction for the next turn |
| `Shift+Enter` | Insert a newline when the terminal reports it distinctly |
| `Ctrl+J` | Insert a newline (works even when the terminal collapses Shift+Enter into Enter) |
| `Alt+Enter` (idle) | Insert a newline |
| `Alt+Enter` (streaming) | Queue a follow-up for when the run finishes |
| `Up` / `Down` | Recall prompt history from the first line, or move through the slash-command menu while it is open; `Down` moves forward and restores the draft |
| `Tab` | Insert the highlighted slash command without running it |
| `Esc` | Jump to the tail when scrolled up, or leave slash-command entry by removing the leading `/` |
| `PgUp` / `PgDn`, `Ctrl+U` / `Ctrl+D` | Scroll history |
| Mouse wheel | Scroll history |
| `Ctrl+E` | Toggle tool output and detailed run summaries |
| `Ctrl+R` | Toggle reasoning text |
| `Ctrl+C` (idle) | Quit |
| `Ctrl+C` (streaming) | Abort the current run and restore queued instructions to the input; press again to quit |
| `y` (approval pending) | Allow once |
| `a` (approval pending) | Allow this exact command for the current session |
| `n` / `Esc` (approval pending) | Deny |
| `Ctrl+C` (approval pending) | Deny, abort the run, and restore queued instructions; press again to quit |

Scrolling up during a streaming run stops automatic following; new output no longer snaps the viewport to the bottom. `Esc` returns to the tail. Sending a message also re-enables following.

Mouse reporting is enabled for wheel scrolling. Hold `Shift` while dragging to use the terminal's native text selection in most terminals.

Some terminals and terminal multiplexers report `Shift+Enter` as a plain `Enter`, which makes the two indistinguishable. Iris asks the terminal for Kitty/modifyOtherKeys key disambiguation, but support depends on the terminal; tmux may need `extended-keys on`. Use `Ctrl+J` (always available) or `Alt+Enter` while idle when `Shift+Enter` submits instead of inserting a newline.

The input box stays editable while Iris is streaming. `Enter` queues a steering instruction (delivered after the current assistant turn finishes its tool calls, before the next LLM request) and `Alt+Enter` queues a follow-up (delivered only when the run would otherwise stop). The next queued steering instruction and follow-up are shown above the prompt, and the status footer counts all queued instructions. Delivered instructions become `you (steer) ›` / `you (follow-up) ›` messages in the transcript. Command completion and delivery semantics are described in [Execution Control](execution.md#execution-control).

## Transcript

- User prompts are plain text with a `you › ` label; they are not interpreted as Markdown.
- Assistant responses are rendered as Markdown while streaming. An open code fence is kept as plain text until its closing fence arrives, so a half-written block does not jump around.
- Reasoning is collapsed to one `⋯ reasoning · ~N tok` line by default. `Ctrl+R` expands it.
- Tool calls are one line: status, tool, target, duration, and a short result (line count, edit delta, exit code). The duration excludes time spent waiting for approval.
  - Failures always show a detail line. A shell command that exits non-zero is treated as a failure even though execution completed.
  - Successful tool output is hidden by default. `Ctrl+E` shows a bounded head/tail and the full-output path when one exists.
- Run outcomes are one line (`✓ done · 2 tools · 3 req · 5.1k tok · 8s`). When files changed, a second line reports the count and points to `/status`. `Ctrl+E` lists the changed files.
- Notices are one-line `·` entries; errors are `✗` entries. Only the text that needs attention is multi-line.

## Approval

The approval panel appears above the input and replaces the normal status hints:

```
  ⚠ approval · shell · 4:32
    rm -rf ./build && go test ./...
    ~/workspace/iris · command is not allowlisted
    [y] allow once   [a] always this session   [n] deny
```

The full command is wrapped, keeping both ends when it is too long to display completely. The workdir, policy reason, and detected programs are shown. The countdown mirrors the policy's approval timeout.

`a` remembers the exact command (including its workdir and kind) for the current session. A different command or workdir asks again, and the allowlist is cleared when the session changes. While approval is pending the prompt is blurred and input does not accept text; scrolling still works.

## Commands

| Command | Purpose |
| --- | --- |
| `/new` | Create a new session. |
| `/resume [id\|n]` | Resume the latest session, one matching an ID prefix, or one from the `/sessions` list (1 = most recent). |
| `/sessions` | List recent sessions with short ID, age, model, and a title derived from the first user message. |
| `/fork [n]` | Fork from the nth user message (1-based); omit `n` for the latest user message. |
| `/export` | Export the current session as Markdown under the session directory. |
| `/status` | Show the full session ID, model, workspace, policy, context usage, the compaction trigger, any wait for the server, session usage, last run, queue, and session directory. |
| `/clear-queue` | Discard queued steering/follow-up instructions and restore their text to the input. Available while streaming. |
| `/skills` | List discovered skills with their trust level. |
| `/skill <name>` | Insert a skill into the input box. Project skills are wrapped as untrusted content. |
| `/prompts` | List prompt templates with their trust level. |
| `/prompt <name> [input]` | Render a template into the input box. Project templates are wrapped as untrusted content. |
| `/help`, `/?` | List commands and keys. Available while streaming. |
| `/quit`, `/exit` | Quit Iris. |

Typing `/` opens a completion menu above the input. `Up`/`Down` move through every match, not only the rows that fit: the menu shows as many rows as the terminal allows and scrolls with the selection, and the status hint reports the position (`↑↓ 7/13`) so the rest of the list stays discoverable. Editing the query moves the highlight back to the first match, and it also undoes an `Esc` dismissal, so the menu returns when the prompt changes.

`Enter` acts on the highlighted command rather than on the bare text that opened the menu: a command that needs no argument runs at once, and one that needs an argument (its usage is written `<name>`) is inserted with a trailing space so the argument can be typed. `Tab` inserts the highlighted command without running it. `Esc` leaves command entry instead of only closing the popup: it removes the leading `/`, so a prompt that was just `/` becomes empty and ready for a normal message, while anything typed after the slash stays as plain text.

Unknown commands produce a notice. Session-mutating commands persist the current session first and are unavailable while a run is active; `/clear-queue`, `/help`, `/status`, and `/quit` remain available. The TUI uses the same durable recorder as the headless modes: user messages, assistant tool calls, and tool results are on disk while the run is still in progress. Recovered sessions show interrupted tool calls in the transcript. Tool history is rebuilt on resume from the recorded results, so a resumed session shows the same tool names, targets, status, and result summaries; per-call durations are not persisted. After each settled run the TUI appends the compact execution summary described in [Execution Summaries](execution.md#execution-summaries); the detailed counters are available through `/status` or `Ctrl+E`.

The read-only detail toggles (`Ctrl+E`, `Ctrl+R`) are per-process and reset on restart.

Markdown uses glamour's dark style by default. For a light terminal, set `GLAMOUR_STYLE=light` (or a glamour style path) before launching Iris.
