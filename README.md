# Iris

**Iris** is a local-first AI agent built in Golang, designed to assist with software development and evolve into a general-purpose autonomous agent. It is optimized specifically for small open models (~9B parameters) running on local hardware.

## How It Works

Iris runs a **deterministic agentic loop** with three states:

1. **`idle`** → Sends a request to the LLM to generate the next response
2. **`tool`** → Executes any tool calls returned by the LLM (read, edit, write, shell)
3. **`done`** → The LLM has finished and loop ends

The agent maintains a conversation history and can make multi-step decisions, calling tools iteratively until the task is complete.

## Built-in Tools

| Tool | Description |
| --- | --- |
| `read` | Read file contents (with line range support) |
| `edit` | Edit files via exact text replacements |
| `write` | Write/create files (creates parent directories) |
| `shell` | Execute shell commands with output capture |

## Requirements

- **Go 1.26.0+**
- **LLM server**: Any OpenAI-compatible API endpoint (e.g. Ollama, llama.cpp, vLLM)
- **Terminal**: TUI requires a terminal emulator (e.g. iTerm2, Alacritty, Ghostty)

## Quick Start

### 1. Build

```bash
make build
```

### 2. Start an LLM server

For example, with llama.cpp:

```bash
llama serve
```

### 3. Configure Iris

Iris auto-creates a `~/.iris/settings.toml` on first run. Edit it to set your LLM. Here is a sample configuration:

```toml
❯ cat ~/.iris/settings.toml
base_url = 'http://127.0.0.1:8080/v1'
max_retries = 3
default_model = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'

[[models]]
name = 'bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 0.6
top_p = 0.95
top_k = 20

[[models]]
name = 'unsloth/Qwen3.5-9B-GGUF:Q5_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 20
min_p = 0.0
repeat_penalty = 1.0
presence_penalty = 1.5

[[models]]
name = 'unsloth/gemma-4-12B-it-GGUF:Q4_K_M'
context_size = 75000
temperature = 1.0
top_p = 0.95
top_k = 64
```

### 4. Run

```bash
./bin/iris
```

Or with CLI flags:

```bash
./bin/iris --debug --workdir ./my-project --base-url http://127.0.0.1:11434/v1 --model model-name
```

## CLI Options

```bash
❯ ./bin/iris --help
iris is an AI agent that can perform various tasks based on user input.

Usage:
  iris [flags]

Flags:
  -b, --base-url string   Set the LLM API URL (Defaults to http://127.0.0.1:8080/v1)
  -d, --debug             Enable debug logging
  -h, --help              help for iris
  -m, --model string      Set the LLM model name (e.g. unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_XL)
  -w, --workdir string    Set the working directory for the agent (Defaults to current directory)
```

## TUI Key Bindings

| Binding | Action |
| --- | --- |
| `Enter` | Send message |
| `Shift+Enter` or `Ctrl+J` | Insert newline |
| `PgUp` / `PgDn` or `Ctrl+U` / `Ctrl+D` | Scroll history |
| `Ctrl+C` | Quit |

## Making Custom Tools

Implement the `tool.Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    FullDescription() string
    Parameters() llm.Parameters
    Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}
```

Register it in a tool package:

```go
func NewMyTool() *MyTool { return &MyTool{} }

func (t *MyTool) Name() string { return "my-tool" }
func (t *MyTool) Description() string { return "Does something useful" }
func (t *MyTool) FullDescription() string { return "Does something useful. Use when..." }
func (t *MyTool) Parameters() llm.Parameters {
    return llm.Parameters{
        Type: "object",
        Required: []string{"input"},
        Properties: map[string]llm.Property{
            "input": {Type: "string", Description: "The input"},
        },
    }
}
func (t *MyTool) Execute(ctx context.Context, args map[string]any) (map[string]any, error) {
    // ...
    return map[string]any{"result": "done"}, nil
}
```

## Development

```bash
❯ make
build - Clean, compile and install
lint - Run linters (goimports, go vet, golangci-lint)
test - Run all tests (with race detection)
coverage - Run tests with coverage report (to ./coverage.out)
```

## My Setup

I'm currently running Iris on a Mac mini M4 with 16GB RAM. I use llama.cpp to serve the following models (context size 75k):

- [bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF:Q5_K_M](https://huggingface.co/bartowski/deepreinforce-ai_Ornith-1.0-9B-GGUF)
- [unsloth/Qwen3.5-9B-GGUF:UD-Q5_K_M](https://huggingface.co/unsloth/Qwen3.5-9B-GGUF) - _reasoning is disabled_
- [unsloth/gemma-4-12B-it-GGUF:Q4_K_M](https://huggingface.co/unsloth/gemma-4-12B-it-GGUF)

## Why Iris Exists

Most modern AI systems assume large cloud models, massive context windows, and API dependency. Iris explores a different direction:

> What if strong agent harness makes it possible to run small open models locally, and still achieve useful results?

## Similar Projects

The only working one I know is [Pi agent harness](https://pi.dev/)

## License

Iris is licensed under the [MIT License](LICENSE).
