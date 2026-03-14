# Iris

**Iris** is a local-first AI agent designed to assist with software development and evolve into a general-purpose autonomous agent - optimized specifically for small open models (~8B parameters) running on local hardware.

Iris focuses on architectural reliability rather than model size. Instead of depending on massive cloud models, it compensates with structured planning, deterministic state management, and strong tool orchestration.

## Vision

Build a **privacy-first, local AI agent framework** that:

* Runs efficiently on consumer hardware
* Works with small open models
* Uses strong architectural scaffolding instead of brute-force intelligence
* Evolves from coding assistant to general autonomous agent

Iris is not a wrapper around an LLM.

It is an **agent system**.

## Design Principles

### 1. Local-First

* Designed for small (~7B–8B) open models
* No cloud dependency required
* Optimized for limited context windows

### 2. Architecture Over Model Size

* Clear separation between planning and execution
* Structured state management
* Explicit tool schema and validation
* Deterministic agent loop

### 3. Structured Reasoning

* Planner proposes steps
* Executor performs atomic actions
* Subtask creator narrows scope for small models
* State is tracked explicitly outside the model

### 4. Extensible Agent Core

Designed to support:

* Reflection loops
* Plan repair
* Persistent memory
* Multi-agent collaboration
* General-purpose task execution

## Current Architecture

```
iris/
├── cli.py                 # Entry point
├── planner/               # Intent analysis & task planning
├── executor/              # Step execution & tool orchestration
├── tools/                 # Tool abstractions (filesystem, bash, etc.)
├── llm/                   # Model client abstraction
└── utils/                 # Logging, JSON repair, helpers
```

### Planner

Responsible for:

* Classifying user intent
* Requesting clarification
* Generating structured proposals

### Executor

Responsible for:

* Executing agent steps
* Managing subtask decomposition
* Calling tools
* Updating state

### Tools

Current tools:

* Filesystem access
* Bash execution

Future tools:

* HTTP client
* Browser automation
* Memory store
* Knowledge retrieval

### LLM Client

Abstracts model interaction so different local models can be swapped easily.

## Target Models

Iris is optimized for:

* ~7B–8B parameter open models
* Quantized variants (4-bit / 8-bit)
* Local inference via engines like llama.cpp, Ollama, etc.

Constraints embraced:

* Limited context window
* Weaker multi-step reasoning
* Stricter JSON enforcement needed

The architecture compensates for these limitations.

## Roadmap

### Phase 0 – Dev Agent (Current)

* Structured planning & execution
* Deterministic tool calls
* Clean state modeling
* Prompt modularization

### Phase 1 – Reliability Layer

* Failure classification
* Automatic plan repair
* Tool result summarization
* Token budget management

### Phase 2 – Memory Layer

* Short-term working memory
* Long-term episodic storage
* Context compression
* Persistent identity

### Phase 3 – Reflective Agent

* Self-evaluation
* Execution scoring
* Replanning loops
* Multi-agent orchestration

### Phase 4 – General Assistant

* Broader task support
* OS-level automation
* Knowledge integration
* Personal assistant capabilities

## Why Iris Exists

Most modern AI systems assume:

* Large cloud models
* Massive context windows
* API dependency

Iris explores a different direction:

> What if strong architecture enables small models to behave intelligently?

This project is an exploration of agent engineering under constraint.

## Installation

```bash
# Clone repository
git clone <repo-url>
cd iris

# Install dependencies
uv sync
```

## Usage

```bash
uv run -m iris.cli --debug "create a snake game"
```

## Development Goals

* Deterministic execution traces
* Reproducible runs
* Model comparison support
* Evaluation harness
* Strict tool schema validation
* Observable agent state

## Long-Term Direction

Iris is intended to become:

* A research playground for small-model agent systems
* A privacy-focused local AI assistant
* A structured agent framework
* Possibly a future open-source platform

## Philosophy

Small models force discipline.

* Explicit state > hidden reasoning
* Structure > improvisation
* Architecture > scale

Iris embraces this constraint deliberately.

## License

See `LICENSE`.
