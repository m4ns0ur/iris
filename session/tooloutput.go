package session

import (
	"fmt"
	"os"
	"path/filepath"
)

var _ ToolOutputProvider = (*JSONLManager)(nil)

// ToolOutputDir returns the persistent directory for full tool output. The
// directory is shared by sessions in this store so paths in a forked or
// resumed transcript remain readable. It is private to the user and is not
// listed as a session.
func (m *JSONLManager) ToolOutputDir(sessionID string) (string, error) {
	if !validID(sessionID) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSessionID, sessionID)
	}

	dir := filepath.Join(m.dir, "tool-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create tool output directory: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	return dir, nil
}
