# Iris

**Iris** is a local-first AI agent built in Go for software development and
general-purpose autonomous work. It is optimized for small open models on local
hardware while keeping execution predictable, inspectable, and durable.

## What Iris provides

- A deterministic agent loop with explicit turn and tool budgets, cancellation,
  steering, and follow-up work.
- A typed event stream shared by the TUI, JSONL, RPC, and Go embedding APIs.
- Durable append-only sessions with writer leases, crash recovery, forking, and
  resume.
- File, shell, and approval boundaries that treat project instructions,
  repository files, shell output, and tool results as untrusted data.
- Shared tool-output management that bounds the model-visible result, preserves
  full output separately, and lets the model retrieve omitted portions.
- Runtime-observed execution summaries with counters, token usage, duration, and
  Git working-tree changes.

## Quick start

Requirements: Go 1.26.1+ and any OpenAI-compatible model server.

```bash
make build
./bin/iris
```

On first run Iris creates `$IRIS_HOME/settings.toml`, which defaults to
`~/.iris/settings.toml`. Set `IRIS_HOME` or pass `--home` to keep Iris files
elsewhere. Add a `default_model` and a matching `[[models]]` entry, then start
the TUI:

```toml
base_url = 'http://127.0.0.1:8080/v1'
default_model = 'local-model'

[[models]]
name = 'local-model'
context_size = 75000
tool_calling = true
```

See [Getting Started](docs/getting-started.md) for the complete first-run flow
and [Configuration Reference](docs/configuration.md) for every setting.

## Documentation

The detailed documentation lives in [`docs/`](docs/README.md).

| Document | Purpose |
| --- | --- |
| [Getting Started](docs/getting-started.md) | Build, configure, and run Iris. |
| [Command-Line Interface](docs/cli.md) | Headless modes and automation. |
| [Configuration Reference](docs/configuration.md) | All configuration keys and environment variables. |
| [Execution Model](docs/execution.md) | Agent loop, control, tool handling, and execution summaries. |
| [Security and Permissions](docs/security.md) | Sandbox, shell policy, approvals, and logging. |
| [Tools](docs/tools.md) | Built-in tools, output management, and custom tools. |
| [Context Management](docs/context.md) | Prompt views, compaction, and token budgets. |
| [TUI](docs/tui.md) | Interactive UI, keys, and commands. |
| [Sessions](docs/sessions.md) | Durable transcripts, recovery, and session tooling. |
| [Resources](docs/resources.md) | Project instructions, trust, skills, and prompts. |
| [Embedding Iris in Go](docs/embedding.md) | Runtime and client APIs. |
| [Agent API](docs/agent-api.md) | Low-level agent API, events, and errors. |
| [Providers](docs/providers.md) | Provider implementations and capabilities. |
| [Development](docs/development.md) | Build, test, lint, and CI. |

## Development

```bash
make help
make test
make lint
```

The default `make` target runs lint and build. See
[Development](docs/development.md) for the full toolchain, test layout, and CI
description.

## Why Iris

Most modern AI systems assume large cloud models, massive context windows, and
API dependency. Iris explores a different direction: a strong local agent
harness that makes small open models useful while preserving explicit control
over tools, permissions, and execution state.

> What if a strong agent harness makes it possible to run small open models
> locally, and still achieve useful results?

## My Setup

I run Iris on a Mac mini M4 with 16 GB RAM and llama.cpp serving these models at
a 75k context size:

- [bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M](https://huggingface.co/bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF)
- [unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_M](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) — _reasoning disabled_
- [unsloth/gemma-4-12B-it-GGUF:Q4_K_M](https://huggingface.co/unsloth/gemma-4-12B-it-GGUF)

## Similar Projects

The only working one I know is [Pi agent harness](https://pi.dev/).

## License

Iris is licensed under the [MIT License](LICENSE).
