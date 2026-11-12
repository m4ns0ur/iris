# Configuration Reference

[← Documentation index](README.md)

---


Configuration is TOML, loaded from `$IRIS_HOME/settings.toml`, which defaults to `~/.iris/settings.toml`. Iris creates the file with defaults on first run. Missing files, directories, and resources are not errors.

## Home directory

Iris keeps agent-owned files under `~/.iris` by default. Move the whole directory with `--home` or `IRIS_HOME`:

```bash
iris --home /path/to/iris-home ...
IRIS_HOME=/path/to/iris-home iris ...
```

`--home` takes precedence over `IRIS_HOME`, and both take precedence over `~/.iris`. Relative paths are resolved against the current directory. The home directory contains `settings.toml`, the global `SYSTEM.md`, global skills and prompts, the default session store, and logs. More specific overrides still apply: `session.dir` (or `IRIS_SESSION_DIR`) for sessions and `IRIS_LOG_DIR` for logs.

Iris does not implement the XDG Base Directory specification. To use an XDG-style location, point `IRIS_HOME` at it, for example `IRIS_HOME="$XDG_CONFIG_HOME/iris"`.

Precedence (highest first):

1. CLI flags (`--base-url`, `--model`)
2. `IRIS_*` environment variables
3. `$IRIS_HOME/settings.toml` (`~/.iris/settings.toml` by default)
4. Built-in defaults

## Top-level

| Key | Default | Purpose |
| --- | --- | --- |
| `base_url` | `http://127.0.0.1:8080/v1` | OpenAI-compatible API root. `--base-url` overrides. |
| `max_retries` | `3` | HTTP attempts for one LLM request (minimum 1). Retries use bounded exponential backoff and apply only before a response stream starts. |
| `default_model` | `""` | Model sent with requests and matched against `[[models]]`. `--model` overrides. |
| `[[models]]` | — | One or more model settings blocks. |

## Model settings (`[[models]]`)

`default_model` is matched case-insensitively against `name`. If no entry matches, Iris logs an error and uses zero-valued settings. Because `--model` replaces `default_model`, passing a model that is not listed in `[[models]]` also produces zero settings: the context window falls back to the default and transport sampling defaults apply.

| Key | Default | Purpose |
| --- | --- | --- |
| `name` | — | Model name sent to the provider. |
| `context_size` | `0` | Model context window used by the context manager. `context.window` overrides it when set; if both are zero, 32768 is used. |
| `tool_calling` | `false` | Descriptive capability metadata (`provider.Capabilities`). |
| `reasoning` | `false` | Descriptive capability metadata. |
| `structured_output` | `false` | Descriptive capability metadata. |
| `vision` | `false` | Descriptive capability metadata. |
| `sampling_parameters` | `[]` | Descriptive list of sampling parameters the model supports. Metadata only. |
| `temperature` | `0` (transport default `0.6`) | Sampling temperature. Zero is treated as unset. |
| `top_p` | `0` (omitted) | Nucleus sampling. Zero is omitted from the request. |
| `top_k` | `0` (omitted) | Top-k sampling. Zero is omitted. |
| `min_p` | `0` (omitted) | Min-p sampling. Zero is omitted. |
| `repeat_penalty` | `0` (omitted) | Repeat penalty. Zero is omitted. |
| `presence_penalty` | `0` (omitted) | Presence penalty. Zero is omitted. |
| `frequency_penalty` | `0` (omitted) | Frequency penalty. Zero is omitted. |

The capability flags and `sampling_parameters` are descriptive only: providers must not assume a feature is supported just because it was declared. Sampling values and `context_size` do affect requests and context management.

## Security (`[security]`)

| Key | Default | Purpose |
| --- | --- | --- |
| `mode` | `workspace-write` | `read-only`, `workspace-write`, or `danger-full-access`. See [File sandbox](security.md#file-sandbox). |
| `external_sandbox` | `false` | Acknowledge that Iris runs inside a container/VM. Required for shell mode `allow`. |
| `read_paths` | `[]` | Additional paths the file tools may read. |
| `write_paths` | `[]` | Additional paths the file tools may write (and therefore read). |
| `shell` | — | Shell policy, described below. |

## Shell (`[security.shell]`)

| Key | Default | Purpose |
| --- | --- | --- |
| `mode` | `approval` | `deny`, `allowlist`, `approval`, or `allow`. See [Shell policy](security.md#shell-policy). |
| `allow` | `[]` | Glob patterns for commands that skip approval. |
| `deny` | `[]` | Glob patterns that are always denied. |
| `disable_default_deny` | `false` | Remove the built-in catastrophic-command deny list. |
| `approval_hook` | `[]` | argv of an external approval program (no shell). |
| `timeout_seconds` | `120` | Timeout for one shell command. |
| `approval_timeout_seconds` | `300` | How long Iris waits for an approval decision. |
| `max_output_bytes` | `25600` (25 KiB) | Shared model-visible tool output cap. Maximum 4 MiB. |
| `max_output_lines` | `1000` | Shared model-visible tool output line cap. Maximum 200,000. |
| `max_capture_bytes` | `10485760` (10 MiB) | Full output stored separately for retrieval. Maximum 100 MiB. |
| `temp_file_ttl_minutes` | `1440` (24 h) | Reaping age for ephemeral tool-output directories left by crashed processes. |
| `binary` | `""` | Shell used for commands. Empty uses `bash` when available, otherwise `sh`. |

These limits are the common tool-output policy and apply to every tool result, not only shell. `max_capture_bytes` is raised to at least `max_output_bytes` if configured lower. Output limits above the maxima are rejected at startup.

## LLM, agent, context, and sessions

| Key | Default | Purpose |
| --- | --- | --- |
| `llm.request_timeout_seconds` | `1800` | Deadline for one complete LLM request, including retries and streaming. |
| `agent.max_turns` | `100` | LLM turns per run. |
| `agent.max_tool_calls` | `200` | Tool executions per run. |
| `agent.tool_timeout_seconds` | `300` | Timeout for one tool execution. |
| `context.window` | `0` | Overrides the model `context_size`. Zero uses the model setting. |
| `context.reserve_tokens` | `8192` | Reserved for the next response; also sent as `max_tokens`. |
| `context.compact_at` | `0.85` | Fraction of the prompt budget that triggers compaction. |
| `context.keep_recent_tokens` | `16384` | Recent conversation kept verbatim during compaction. |
| `context.summary_max_tokens` | `2048` | Output budget for compaction summaries. |
| `session.dir` | `""` | Session store directory. Empty uses `$IRIS_HOME/sessions`, which defaults to `~/.iris/sessions`. |

## Environment variables

Every known configuration key can be overridden with an `IRIS_`-prefixed environment variable; dots become underscores and keys are upper-cased. For example:

```bash
IRIS_HOME=/path/to/iris-home
IRIS_LOG_DIR=/path/to/logs
IRIS_SESSION_DIR=/path/to/sessions
IRIS_BASE_URL=http://127.0.0.1:11434/v1
IRIS_DEFAULT_MODEL='unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
IRIS_SECURITY_MODE=read-only
IRIS_SECURITY_SHELL_MODE=deny
IRIS_SECURITY_SHELL_TIMEOUT_SECONDS=60
IRIS_AGENT_MAX_TURNS=50
IRIS_CONTEXT_RESERVE_TOKENS=4096
```

`IRIS_HOME` and `IRIS_LOG_DIR` are process-level path overrides rather than configuration keys. Viper resolves environment variables for keys that are known through the config file or built-in defaults. The `[[models]]` list is not set through environment variables.
