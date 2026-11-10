# Development

[← Documentation index](README.md)

---


```bash
❯ make help
lint      - Run linters (goimports, go vet, golangci-lint)
build     - Clean, compile and install
test      - Run all tests (with race detection)
coverage  - Run tests with coverage report (to ./output/coverage.out)
```

| Command | Effect |
| --- | --- |
| `make` | Default target: `lint` then `build`. |
| `make all` | `lint`, `build`, `test`, `coverage`. |
| `make build` | Clean, tidy modules, build `./bin/iris` with stripped/trimmed flags, and `go install`. |
| `make lint` | `goimports -w .`, `go vet ./...`, `golangci-lint run`. |
| `make test` | `go test -race -short ./...`. |
| `make coverage` | Coverage profile at `./output/coverage.out` (no race detector). |
| `make help` | Print the documented targets. |

`make lint` requires `goimports` and `golangci-lint` on `PATH`.

CI (`.github/workflows/go.yml`) runs `go build -v ./...` and `go test -v ./...` on pushes and pull requests to `main`, using the Go version from `go.mod`.

## Repository layout

| Path | Contents |
| --- | --- |
| `agent/` | Public agent contract, typed event stream, approvals, and errors. |
| `client/` | Session-aware headless API used by print, JSONL, and RPC. |
| `runtime/` | Provider, tool, policy, and budget composition root. |
| `provider/` | Provider interfaces; `provider/openai/` is the HTTP transport. |
| `tool/` | Public custom-tool and environment interfaces. |
| `session/` | Session model, JSONL storage, leases, recovery, and exports. |
| `resource/` | Markdown instruction discovery, trust, skills, and prompts. |
| `rpc/` | JSON-RPC server. |
| `internal/` | Implementation packages: agent loop, tools, policy, context manager, TUI, CLI, persistence, config, and logging. |
| `docs/` | User and embedder documentation. |

Tests live beside the packages they exercise as `*_test.go` files; there is no separate test tree. `make test` runs the race detector with the short flag, and CI runs the full package tests.
