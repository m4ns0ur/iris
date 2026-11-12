# Getting Started

[← Documentation index](README.md)

---


## Requirements


- **Go 1.26.1+** (see `go.mod`)
- **LLM server**: any OpenAI-compatible API endpoint (llama.cpp, Ollama, vLLM, LM Studio, or a hosted compatible API)
- **Terminal**: the TUI requires a terminal emulator (iTerm2, Alacritty, Ghostty, …). Headless modes do not.

On Unix, shell commands and approval hooks run in their own process group and are killed as a group on timeout or cancellation. On other platforms Iris can only terminate the direct child process.


## Quick Start


### 1. Build

```bash
make build
```

The default `make` target runs lint first, then build. See [Development](development.md#development) for all targets.

### 2. Start an LLM server

For example, with llama.cpp:

```bash
llama-server -m /path/to/model.gguf --port 8080 --ctx-size 75000
```

Any OpenAI-compatible endpoint works; set `base_url` accordingly (Ollama, for example, exposes `http://127.0.0.1:11434/v1`).

### 3. Configure Iris

On first run Iris creates `$IRIS_HOME/settings.toml`, which defaults to `~/.iris/settings.toml`, containing the defaults but no models. To keep Iris files outside your home directory, set `IRIS_HOME` or pass `--home`. Add at least a `default_model` and a matching `[[models]]` entry:

```toml
base_url = 'http://127.0.0.1:8080/v1'
max_retries = 3
default_model = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'

[[models]]
name = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'
context_size = 75000
# Descriptive capabilities. See the Providers documentation.
tool_calling = true
reasoning = true
structured_output = false
vision = false
sampling_parameters = ['temperature', 'top_p', 'top_k']
temperature = 0.6
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 0.0
frequency_penalty = 0.0

[[models]]
name = 'unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 1.5

[security]
# read-only | workspace-write | danger-full-access
mode = 'workspace-write'
# Acknowledge that Iris runs inside a container/VM. Required for shell mode 'allow'.
external_sandbox = false
# read_paths = ['/some/shared/read-only-dir']
# write_paths = ['/some/shared/writable-dir']

[security.shell]
# deny | allowlist | approval | allow
mode = 'approval'
# Commands/patterns that skip approval. Patterns with spaces match the command
# or one segment; patterns without spaces match executable names.
allow = ['ls', 'pwd', 'git status*', 'git diff*', 'git log*']
# Always denied, even if approved.
deny = ['curl*', 'wget*']
# Remove the built-in catastrophic-command deny list (explicit opt-out).
# disable_default_deny = true
timeout_seconds = 120
approval_timeout_seconds = 300
# Shared model-visible tool-output limits (apply to every tool).
max_output_bytes = 25600
max_output_lines = 1000
max_capture_bytes = 10485760
temp_file_ttl_minutes = 1440
# Optional external approval hook (argv, no shell). Exit 0 = allow.
# approval_hook = ['/usr/local/bin/iris-approve']
# Optional shell binary. Empty uses bash when available, otherwise sh.
# binary = '/bin/bash'

[session]
# Empty uses $IRIS_HOME/sessions (~/.iris/sessions by default).
dir = ''

[context]
# Zero uses the selected model's context_size.
window = 0
# Reserved for the next assistant response and used as the request output cap.
reserve_tokens = 8192
# Compact when prompt tokens exceed this fraction of (window - reserve_tokens).
compact_at = 0.85
# Recent conversation kept verbatim during compaction.
keep_recent_tokens = 16384
# Output budget for generated summaries.
summary_max_tokens = 2048

[llm]
# Deadline for one complete request, including retries and streaming.
request_timeout_seconds = 1800

[agent]
# Per-run execution budgets.
max_turns = 100
max_tool_calls = 200
tool_timeout_seconds = 300
```

### 4. Run

```bash
./bin/iris
```

Or with CLI flags:

```bash
./bin/iris --debug --workdir ./my-project \
  --base-url http://127.0.0.1:11434/v1 \
  --model model-name
```

`--workdir` sets the workspace used by tools, shell commands, and project resources. It does not change the process working directory or Iris's agent directory, so logs are still written under `$IRIS_HOME/logs/` (`~/.iris/logs/` by default, one file per session).
