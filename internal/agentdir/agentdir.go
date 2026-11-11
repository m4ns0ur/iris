// Package agentdir resolves Iris's per-user directory. Iris keeps all of its
// agent-owned files (configuration, resources, sessions, logs) under a single
// directory in the user's home so that workspaces stay free of runtime state.
package agentdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Name is the per-user agent directory created inside the user's home.
const Name = ".iris"

const (
	logsDir     = "logs"
	sessionsDir = "sessions"
)

// Dir returns the agent directory, usually ~/.iris.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, Name), nil
}

// Logs returns the log directory inside the agent directory, usually
// ~/.iris/logs. The directory is not created.
func Logs() (string, error) {
	return subdir(logsDir)
}

// Sessions returns the default session store directory, usually
// ~/.iris/sessions. The directory is not created.
func Sessions() (string, error) {
	return subdir(sessionsDir)
}

func subdir(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Ensure creates dir and any missing parents with owner-only permissions.
// MkdirAll does not tighten permissions on an existing directory, so the
// permissions are applied explicitly.
func Ensure(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
