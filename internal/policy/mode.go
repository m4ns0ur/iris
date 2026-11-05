package policy

import (
	"fmt"
	"strings"
)

// Mode controls which paths the file tools may access.
type Mode string

const (
	// ModeReadOnly allows reads inside the workspace but rejects all writes
	// and disables the shell tool.
	ModeReadOnly Mode = "read-only"

	// ModeWorkspaceWrite allows reads and writes inside the workspace (and
	// any explicitly configured extra roots) and rejects everything else.
	ModeWorkspaceWrite Mode = "workspace-write"

	// ModeDangerFullAccess removes path confinement entirely. It must be
	// selected explicitly and is intended for environments where the process
	// is already isolated by an external sandbox.
	ModeDangerFullAccess Mode = "danger-full-access"
)

// ParseMode normalises a configured security mode. An empty value defaults to
// workspace-write.
func ParseMode(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(ModeWorkspaceWrite):
		return ModeWorkspaceWrite, nil
	case string(ModeReadOnly):
		return ModeReadOnly, nil
	case string(ModeDangerFullAccess):
		return ModeDangerFullAccess, nil
	default:
		return "", fmt.Errorf(
			"%w: unknown security mode %q (want %q, %q or %q)",
			ErrInvalidPolicy,
			value,
			ModeReadOnly,
			ModeWorkspaceWrite,
			ModeDangerFullAccess,
		)
	}
}

// AllowsWrite reports whether the mode permits write operations.
func (m Mode) AllowsWrite() bool {
	return m == ModeWorkspaceWrite || m == ModeDangerFullAccess
}

// String implements fmt.Stringer.
func (m Mode) String() string {
	if m == "" {
		return string(ModeWorkspaceWrite)
	}
	return string(m)
}
