# Providers

[← Documentation index](README.md)

---


Providers stream typed events:

```go
type Provider interface {
    Stream(ctx context.Context, request Request) (<-chan StreamEvent, error)
}

type StreamEvent interface { streamEvent() }

type StartedEvent struct { At time.Time }
type DeltaEvent struct { Delta Delta }
type DoneEvent struct { Response Response }
type ErrorEvent struct { Err error }
```

A stream contains an optional `StartedEvent`, then zero or more `DeltaEvent`s, then exactly one `DoneEvent` or `ErrorEvent`. Send `StartedEvent` as soon as the server has accepted the request and the response body is open, before reading content: a server that is still loading a model or pre-filling a prompt sends nothing for a long time, and consumers use this boundary to tell that silence apart from generation. `provider.Collect` ignores it, and providers that never send one still work; the runtime then attributes the whole request to waiting. Setup failures are returned directly from `Stream`; failures after the stream starts arrive as `ErrorEvent`. The channel must be closed after the terminal event, and context cancellation must stop the request. The `StreamEvent` interface has an unexported method, so external packages use the exported event types rather than defining new ones.

OpenAI-compatible Chat Completions is the first implementation, available through [`provider/openai`](../provider/openai). `provider.Collect` consumes a stream and returns the final response; it reports an error if the channel closes without a terminal event. `provider.ProviderFunc` adapts a stream function; `provider.ResponseFunc` adapts a non-streaming function and emits a `StartedEvent` followed by a single `DoneEvent`.

`openai.New(openai.Options{...})` accepts:

| Option | Default | Purpose |
| --- | --- | --- |
| `BaseURL` | `http://127.0.0.1:8080/v1` | API root; `/chat/completions` is appended. |
| `APIKey` | `""` | Bearer token, when set. |
| `Model` | `""` | Default model name. |
| `MaxRetries` | `3` | HTTP attempts (minimum 1). |
| `RequestTimeout` | 30 minutes | Deadline for one complete request. |
| `HTTPClient` | no timeout | Optional custom HTTP client. |

Providers may stream deltas or return the complete assistant text only in the terminal `DoneEvent`; the runtime emits synthetic `assistant.delta`/`reasoning.delta` events in the latter case so every consumer sees the same stream shape.

The transport sets `stream=true` and requests usage in the stream. Non-transient 4xx responses are not retried; 408, 425, 429, and 5xx responses are retried with bounded exponential backoff (1s, 2s, 4s, … capped at 30s), honoring `Retry-After`. Retries stop once an HTTP 200 stream has started: a disconnect after that point produces `provider.ErrStreamInterrupted` rather than replaying the request. Malformed or contradictory stream data produces `provider.ErrInvalidStream`. A 413 response or an error that looks like a context-window overflow is wrapped in `provider.ErrContextWindow` and handled by forced compaction in the runtime.

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
