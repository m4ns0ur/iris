package policy

import "errors"

// Sentinel errors returned by the policy layer. Tools and callers can use
// errors.Is to distinguish policy denials from ordinary execution failures.
var (
	// ErrPathOutsideWorkspace is returned when a path resolves outside every
	// root the current policy allows.
	ErrPathOutsideWorkspace = errors.New("path is outside the allowed workspace")

	// ErrReadOnlyMode is returned when a write is attempted in read-only mode.
	ErrReadOnlyMode = errors.New("write access denied: security mode is read-only")

	// ErrPathResolution is returned when a path cannot be resolved safely,
	// for example because it contains a dangling symlink or a symlink loop.
	ErrPathResolution = errors.New("cannot safely resolve path")

	// ErrShellDisabled is returned when shell execution is disabled by the
	// security mode or shell policy.
	ErrShellDisabled = errors.New("shell execution is disabled by policy")

	// ErrCommandDenied is returned when a command is denied by a rule or by
	// the configured approver.
	ErrCommandDenied = errors.New("command denied by policy")

	// ErrApprovalUnavailable is returned when a command needs approval but no
	// approval mechanism is configured. Iris fails closed in this case.
	ErrApprovalUnavailable = errors.New("no approval mechanism is available")

	// ErrApprovalTimeout is returned when an approval request is not answered
	// within the configured timeout.
	ErrApprovalTimeout = errors.New("approval request timed out")

	// ErrInvalidPolicy is returned for malformed security settings.
	ErrInvalidPolicy = errors.New("invalid security policy")
)
