# Iris Documentation

This directory contains the detailed documentation for Iris. The top-level
[README](../README.md) is the short project overview and entry point.

## Start here

| Document | What it covers |
| --- | --- |
| [Getting Started](getting-started.md) | Requirements, installation, first configuration, and first run. |
| [Command-Line Interface](cli.md) | CLI flags and the print, JSONL, and RPC modes, including headless approvals. |
| [Configuration Reference](configuration.md) | Every configuration key, defaults, precedence, and environment variables. |

## Core behavior

| Document | What it covers |
| --- | --- |
| [Execution Model](execution.md) | The agent loop, execution control, model response and tool-call handling, structured tool errors, lifecycle events, and execution summaries. |
| [Security and Permissions](security.md) | File sandbox, shell policy, approvals, limits, cancellation, and logging. |
| [Tools](tools.md) | Built-in tools, shared tool-output management and retrieval, and custom tools. |
| [Context Management](context.md) | Prompt views, compaction, token budgeting, and context-overflow recovery. |

## Interfaces

| Document | What it covers |
| --- | --- |
| [TUI](tui.md) | Key bindings, commands, streaming interaction, and session management. |
| [Sessions](sessions.md) | Durable JSONL sessions, crash recovery, schema, leases, exports, and tool-output files. |
| [Resources](resources.md) | `SYSTEM.md`, `AGENTS.md`, project trust, skills, prompt templates, and resource limits. |

## Embedding and development

| Document | What it covers |
| --- | --- |
| [Embedding Iris in Go](embedding.md) | Embedding example, `runtime.Options`, and the `client` API. |
| [Agent API](agent-api.md) | The low-level `agent` package, typed events, and the error model. |
| [Providers](providers.md) | Provider interface, streaming semantics, retries, and capabilities. |
| [Development](development.md) | Build, test, lint, CI, and repository layout. |

## Conventions

- Examples use `iris` for an installed binary; from a source checkout use
  `./bin/iris`.
- Paths are relative to the workspace unless stated otherwise.
- Commands, tool results, project instructions, and repository content are
  untrusted data unless the security documentation explicitly says otherwise.
