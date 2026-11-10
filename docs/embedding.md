# Embedding Iris in Go

[← Documentation index](README.md)

---


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
defer c.Close()

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

The lower-level [`agent`](../agent) package can be used directly when session management is not needed. `client` adds session-aware runs and JSON-friendly event envelopes; `rpc.NewServer(c).Serve(ctx, in, out)` exposes the same client over JSON-RPC.

## `runtime.Options`

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
| `Resources` | `nil` | Trusted user-level and untrusted project instruction resources (`SYSTEM.md`, `AGENTS.md`, skills, prompts). |
| `Security` | secure defaults | Filesystem and shell boundary. |
| `Approver` | `nil` | Optional initial approval mechanism. |

`runtime.New` validates the security policy up front, so configuration errors surface before the first run. `Runtime.Warnings()` exposes security warnings, and `Close` closes every agent created by the runtime. `NewAgent` returns `runtime.ErrRuntimeClosed` after `Close`.

## `client` API

`client.Options` fields: `Agent` (required), `Sessions` (nil disables persistence), `SessionDefaults`, and `InitialSession` (resume an existing `*session.Session`). With persistence enabled, the client acquires the session writer lease at construction and releases it on `Close`. When the manager implements `session.ToolOutputProvider` and the agent implements `agent.ToolOutputAgent`, the client also points full tool output at the session tool-output directory.

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
| `Close()` | Persist pending metadata and release the writer lease. Returns `ErrBusy` during a run. |

`client.Result`:

| Field | Meaning |
| --- | --- |
| `Content` | Final assistant text of the last turn (falls back to the last assistant message in history). |
| `Reasoning` | Final reasoning text, when the model produced one. |
| `SessionID` | Active session ID. |
| `Usage` | Token usage summed across turns in the run. |
| `ContextSummaryUsage` | Token usage spent on compaction summaries, when any. |
| `Canceled` | Whether the run ended because it was cancelled. |
| `Truncated` | Whether the last model turn hit the output token limit. |
| `Summary` | Runtime-observed execution summary: timing, counters, usage, and working-tree changes. |

`client.ErrBusy` is returned when a run is requested while another is active. Mutating session operations are also busy-checked. With a real agent that implements `agent.RecorderAgent`, the client installs a recorder so user messages, assistant tool calls, and tool results are persisted at their durability points; agents without recorder support use the end-of-run history fallback. `RunStream` handlers run synchronously in the agent run goroutine and must return quickly. `client.EventRecord{Type, Event}` and `client.Record(event)` provide the stable JSON envelope used by JSONL and RPC. When a turn is retried, the client discards partial output from the failed attempt; consumers that maintain their own buffers should reset them on `agent.turn.retried`.
