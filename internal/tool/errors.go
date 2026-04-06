package tool

import "errors"

// ErrToolNotFound is returned when a requested tool doesn't exist.
var ErrToolNotFound = errors.New("tool not found")

// ErrInvalidArgs is returned when tool arguments are invalid.
var ErrInvalidArgs = errors.New("invalid tool arguments")

// ErrExecutionFailed is returned when tool execution fails.
var ErrExecutionFailed = errors.New("tool execution failed")
