# Context Management

[← Documentation index](README.md)

---


Iris builds a prompt view for each model call instead of mutating the canonical conversation. The full, uncompressed conversation remains in the agent history and in the durable session transcript. Compaction only affects what is sent to the model for that request; it never deletes or rewrites session entries.

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

If the estimated prompt still cannot fit after proactive compaction, or the provider rejects the prompt as exceeding its context window, Iris forces an aggressive compaction and retries the request once. Provider implementations should wrap `provider.ErrContextWindow` when they can classify this error; Iris also recognizes common provider error text. The failed attempt contributes no assistant message; `agent.turn.retried` tells consumers to discard any partial output before the retry. If the compacted prompt still cannot fit the budget, the run fails with a context-overflow error.

Context settings are normalized before use:

- the window is at least 1024 tokens;
- `reserve_tokens` is reduced to a quarter of the window when it would leave no prompt budget, and is at least 128;
- `compact_at` is clamped to `0.5`–`0.95`;
- `keep_recent_tokens` is clamped to at most the prompt budget and at least a quarter of it;
- `summary_max_tokens` is capped at half the prompt budget and is at least 128.

The normalized reserve, not the raw configured value, is what is sent as `max_tokens`.

Compaction is observable through `context.compaction.started` and `context.compaction.finished`. The finished event reports before/after estimates, summarized message count, trimmed tool-result count, summary token usage, whether the summary was a fallback, and whether compaction was forced. The TUI shows a live "Compacting context..." status while the pass runs and a notice with before/after estimates when it finishes. Summary token usage is included in the session aggregate. Session files still contain the full original conversation, so resuming a session never loses the uncompressed history.
