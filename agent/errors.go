package agent

import "errors"

var (
	// ErrAgentBusy is returned when a run is requested while another run is
	// already active.
	ErrAgentBusy = errors.New("agent is already running")

	// ErrAgentClosed is returned after Close has been called.
	ErrAgentClosed = errors.New("agent is closed")

	// ErrAgentIdle is returned when a steering instruction or follow-up is
	// submitted while no run is active.
	ErrAgentIdle = errors.New("agent is not running")

	// ErrEmptyInput is returned when an instruction contains only whitespace.
	ErrEmptyInput = errors.New("instruction is empty")

	// ErrEmptyResponse is returned when a model response is empty in a way
	// that cannot be treated as a normal stop (for example, an empty response
	// cut off by the output token limit).
	ErrEmptyResponse = errors.New("empty model response")

	// ErrContextOverflow is returned when even a compacted prompt cannot fit
	// within the configured context window.
	ErrContextOverflow = errors.New("context cannot fit within the model window")

	// ErrTurnBudgetExceeded is returned when a run reaches its LLM turn
	// budget.
	ErrTurnBudgetExceeded = errors.New("agent turn budget exceeded")

	// ErrToolBudgetExceeded is returned when a run reaches its tool execution
	// budget.
	ErrToolBudgetExceeded = errors.New("tool execution budget exceeded")

	// ErrToolTimeout is returned when a single tool execution exceeds the
	// configured tool timeout.
	ErrToolTimeout = errors.New("tool execution timed out")
)
