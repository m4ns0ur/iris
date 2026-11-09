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
