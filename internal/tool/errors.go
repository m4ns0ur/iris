package tool

import "errors"

var (
	// ErrToolNotFound is returned when a requested tool doesn't exist.
	ErrToolNotFound = errors.New("tool not found")

	// ErrInvalidArgs is returned when tool arguments are invalid.
	ErrInvalidArgs = errors.New("invalid tool arguments")

	// ErrExecutionFailed is returned when tool execution fails.
	ErrExecutionFailed = errors.New("tool execution failed")
)
