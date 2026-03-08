# Iris Agent Architecture

This document describes the agent architecture, design patterns, and implementation details of Iris.

## Overview

Iris is a **local-first AI agent** that bridges the gap between small open models (~7B-8B parameters) and complex task execution. Rather than relying on massive cloud models, Iris uses **structured architecture** to compensate for model limitations.

### Core Philosophy

> **Architecture over Scale**: Strong architectural scaffolding enables small models to exhibit intelligent, reliable behavior.

## Agent Loop

```
┌─────────────────────────────────────────────────────────────┐
│                        AGENT LOOP                            │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│   User Intent ──► [Classifier] ──► [Planner] ──► [Executor] │
│           │            │              │                │    │
│           ▼            ▼              ▼                ▼    ▼
│      Task Type   Clarifications    Proposal      Subtasks  Tools
│      Detection                              + State
│                                                              │
│   ┌─────────────────────────────────────────────────────┐   │
│   │              State Management Layer                  │   │
│   │   - ExecutionState (deterministic, explicit)          │   │
│   │   - Semantic summaries (no raw logs)                  │   │
│   │   - Step counting & budget enforcement                │   │
│   └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## Component Architecture

### 1. Classifier (`iris/planner/classifier.py`)

**Purpose**: Classify user intent into task types to determine execution strategy.

**Task Types**:
- `atomic`: Single-step tasks that can be executed directly
- `complex`: Multi-step tasks requiring planning and decomposition

**Implementation**:
```python
class Classifier:
    def build(self, intent: str) -> Classification:
        # 1. Load prompt template
        # 2. Generate LLM response (temperature=0.1)
        # 3. Extract JSON using extract_json()
        # 4. Validate against Classification schema
        pass
```

**Key Design Decisions**:
- Uses **low temperature (0.1)** for deterministic classification
- Returns **structured JSON** for reliable parsing
- Catches edge cases explicitly

### 2. Clarification Builder (`iris/planner/clarification.py`)

**Purpose**: Ask clarifying questions for ambiguous or underspecified tasks.

**Implementation**:
```python
class ClarificationBuilder:
    def build(self, intent: str) -> ClarificationSet:
        # Generate questions with recommended defaults
        # Returns structured ClarificationSet
        pass
```

**Workflow**:
1. Present questions to user with recommended defaults
2. User can accept defaults or provide custom answers
3. Answers passed to ProposalBuilder

### 3. Proposal Builder (`iris/planner/proposal.py`)

**Purpose**: Generate a detailed project proposal including architecture, scope, and implementation steps.

**Output Structure** (`Proposal`):
- **Goal Summary**: High-level objective
- **Architecture**: Language, runtime, framework, entrypoint
- **Scope**: Core features, optional features, explicitly excluded features
- **Uncertainties**: Known unknowns requiring user attention
- **Implementation Steps**: Ordered list of steps with "produces" outputs

**Implementation**:
```python
class ProposalBuilder:
    def build(self, intent: str, clarifications: dict) -> Proposal:
        # Generate comprehensive plan
        # Returns Proposal object
        pass
```

### 4. Subtask Creator (`iris/executor/subtask_creator.py`)

**Purpose**: Break down the proposal into executable atomic subtasks.

**Output**: Ordered list of subtasks, each with:
- `step`: Step number
- `description`: Executable instruction
- `produces`: Expected outputs/artifacts

### 5. Executor (`iris/executor/executor.py`)

**Purpose**: Execute the agent loop for atomic or multi-step tasks.

**Key Features**:

#### State Management
```python
class ExecutionState:
    - user_intent: Original user request
    - shared_context: Proposal (if available)
    - step_count: Tracks progress (enforces max_steps)
    - state_summary: Semantic summaries (not raw logs)
    - last_tool_result: Most relevant tool output
```

#### Reasoning Loop
```python
def run(self, intent: str, shared_context: None) -> str:
    while step_count < max_steps:
        1. Increment step count
        2. Call _reason() to get action
        3. If FinalAnswer: Check goal satisfaction
        4. If ToolAction: Execute tool, update state
        5. Handle truncation of noisy outputs
```

#### Goal Satisfaction Check
```python
def _goal_satisfied(self, response: FinalAnswer) -> bool:
    # Rejects responses containing:
    # "should", "need to", "would need"
    # These indicate more work is needed
    pass
```

### 6. Tool Registry (`iris/tools/registry.py`)

**Purpose**: Centralized tool management and dispatch.

**Registration**:
```python
registry = ToolRegistry()
registry.register("filesystem", FileSystemTool(workspace))
registry.register("shell", ShellTool(workspace))
```

**Dispatch**:
```python
result = tools.dispatch(ToolCall(tool="shell.run", arguments={...}))
```

## Data Models

### Planner Models

#### `Classification`
```python
{
    "task_type": "atomic" | "complex",
    "reasoning": "Explanatory text"
}
```

#### `ClarificationSet`
```python
{
    "questions": [
        {
            "question": "String",
            "recommended_default": "String",
            "required": bool
        }
    ]
}
```

#### `Proposal`
```python
{
    "goal_summary": "String",
    "architecture": {
        "language": ["String"],
        "runtime": ["String"],
        "framework": ["String"],
        "entrypoint": "String"
    },
    "scope": {
        "core_features": ["String"],
        "optional_features": ["String"],
        "explicitly_excluded": ["String"]
    },
    "uncertainties": ["String"],
    "implementation_steps": [
        {
            "step": int,
            "description": "String",
            "produces": ["String"]
        }
    ]
}
```

### Executor Models

#### `ExecutionState`
- See above in Executor section

#### `AgentResponse` (Union)
```python
FinalAnswer: {
    "type": "final",
    "message": "String"
}

ToolAction: {
    "type": "tool",
    "call": {
        "tool": "string",
        "arguments": {...}
    }
}
```

#### `ToolResult`
```python
{
    "tool": "string",
    "success": bool,
    "stdout": "String (truncated to 2000 chars)",
    "stderr": "String"
}
```

## Prompt Engineering

### Classifier Prompt
- **Temperature**: 0.1 (deterministic)
- **Format**: JSON output
- **Focus**: Task type classification only

### Clarification Prompt
- **Temperature**: 0.2 (slightly creative)
- **Format**: JSON array of questions
- **Focus**: Identify ambiguities

### Proposal Prompt
- **Temperature**: 0.15 (balanced)
- **Format**: JSON structure
- **Focus**: Comprehensive planning

### Executor Prompts
- **Atomic**: `iris/executor/prompts/atomic_executor.md`
- **Multi-step**: `iris/executor/prompts/executor.md`
- **Temperature**: Low for determinism
- **Context**: State summary + last tool result (no raw logs)

## State Management Strategy

### Why Semantic Summaries?

Instead of feeding raw tool output logs to the model, we maintain a **semantic summary**:

```python
# BAD: Raw logs (noisy, hard to reason about)
state_summary = [
    "ls -la /path/to/dir\nOutput: drwxr-xr-x ...",
    "cat file.txt\nOutput: def foo():\n...",
    "python script.py\nOutput: Traceback...: error: ..."
]

# GOOD: Semantic summaries (clean, actionable)
state_summary = [
    "Directory /path/to/dir inspected.",
    "File file.txt read: contains function definition.",
    "Script executed successfully."
]
```

### Benefits:
1. **Reduced Token Usage**: Summaries are concise
2. **Improved Focus**: Model sees intent, not noise
3. **Easier Debugging**: Clear progression of state
4. **Better Reasoning**: Model can reason about outcomes

## Tool Abstractions

### Shell Tool
```python
class ShellTool:
    ALLOWED_COMMANDS = ["echo", "cat", "ls", "cd", "mkdir", "rm", ...]
    
    def run(self, command: List[str]) -> ToolResult:
        # Execute with timeout
        # Truncate output
        # Return structured result
        pass
```

### Filesystem Tool
```python
class FileSystemTool:
    def write(self, path: str, content: str) -> ToolResult
    def mkdir(self, path: str) -> ToolResult
    def read(self, path: str) -> ToolResult
```

## Error Handling

### JSON Repair
```python
def extract_json(raw_output: str) -> dict:
    """
    Extract JSON from potentially malformed LLM output.
    Handles:
    - Markdown code blocks (```json ... ```)
    - Incomplete JSON
    - Trailing text
    - Extra whitespace
    """
    # Implementation in iris/utils/json_utils.py
    pass
```

### Tool Output Truncation
```python
MAX_TOOL_OUTPUT = 2000

# Before updating state:
if result.stdout and len(result.stdout) > MAX_TOOL_OUTPUT:
    result.stdout = result.stdout[:MAX_TOOL_OUTPUT] + "\n...[truncated]"
```

### Step Budget Enforcement
```python
MAX_STEPS = {
    "atomic": 5,
    "multi_step": 15,
    "complex": 50
}

# Loop terminates when:
# - Goal satisfied
# - Max steps reached
```

## Extension Points

### Adding New Tools
1. Create tool class in `iris/tools/`
2. Register in `ToolRegistry`
3. Add semantic summary logic in `Executor._update_state_summary()`
4. Ensure proper error handling in `ToolResult`

### Adding New Task Types
1. Update `Classifier` prompt to recognize new type
2. Add type to `Classification` model
3. Handle in `Executor.run()` loop
4. Implement task-specific logic in `_reason()`

### Adding Memory Layer (Phase 2)
Current state is ephemeral. Future additions:
- Short-term working memory
- Long-term episodic storage
- Context compression
- Persistent identity

## Design Patterns Used

### 1. Strategy Pattern
Different execution strategies based on task classification:
- Atomic: Direct execution
- Complex: Plan → Clarify → Execute

### 2. State Pattern
Explicit state management through `ExecutionState`:
- Deterministic transitions
- Observable state changes
- Budget enforcement

### 3. Builder Pattern
Complex objects built in stages:
- `ClarificationBuilder`: Builds questions
- `ProposalBuilder`: Builds project plan
- `SubtaskCreator`: Builds execution steps

### 4. Factory Pattern
Tool creation and registration:
- `ToolRegistry`: Centralized tool management
- `build_executor()`: Consistent executor construction

### 5. Observer Pattern
State updates trigger semantic summary updates:
- Tool actions → State updates
- No tight coupling to specific tools

## Future Enhancements (Roadmap)

### Phase 1: Reliability Layer
- [ ] Failure classification
- [ ] Automatic plan repair
- [ ] Tool result summarization
- [ ] Token budget management

### Phase 2: Memory Layer
- [ ] Short-term working memory
- [ ] Long-term episodic storage
- [ ] Context compression
- [ ] Persistent identity

### Phase 3: Reflective Agent
- [ ] Self-evaluation
- [ ] Execution scoring
- [ ] Replanning loops
- [ ] Multi-agent orchestration

### Phase 4: General Assistant
- [ ] Broader task support
- [ ] OS-level automation
- [ ] Knowledge integration
- [ ] Personal assistant capabilities

## Testing Strategy

### Unit Tests
- Individual component behavior
- State transitions
- Tool dispatch

### Integration Tests
- Full agent loop
- Multi-step execution
- Error scenarios

### Model Comparison
- Deterministic runs with different models
- Temperature impact analysis
- Prompt variations

## Dependencies

### Core
- `typer`: CLI framework
- `pydantic`: Data validation
- `rich`: Terminal formatting

### LLM
- `llama-cpp-python`: Local inference
- `ollama` or other local model servers

### Tooling
- `ruff`, `mypy`, `pytest`: Code quality
- `uv`: Package management

## References

- [README.md](README.md) - Project overview
- [pyproject.toml](pyproject.toml) - Dependencies
- [`iris/`](iris/) - Source code structure