// Package agentdir resolves Iris's per-user directory. Iris keeps all of its
// agent-owned files (configuration, resources, sessions, logs) under a single
// directory so that workspaces stay free of runtime state.
package agentdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Name is the default agent directory created inside the user's home.
const Name = ".iris"

// HomeEnv overrides the agent directory. It is resolved before the home
// directory fallback.
const HomeEnv = "IRIS_HOME"

const (
	logsDir     = "logs"
	sessionsDir = "sessions"
)

var (
	overrideMu sync.RWMutex
	override   string
)

// SetHome overrides the agent directory for this process. An empty dir clears
// the override. Relative paths are resolved against the current directory.
func SetHome(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		setOverride("")
		return nil
	}

	absolute, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve home directory %q: %w", dir, err)
	}
	setOverride(absolute)
	return nil
}

func setOverride(dir string) {
	overrideMu.Lock()
	override = dir
	overrideMu.Unlock()
}

func currentOverride() string {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	return override
}

// Dir returns the agent directory. The precedence is the process override set
// with SetHome, then IRIS_HOME, then ~/.iris.
func Dir() (string, error) {
	if dir := currentOverride(); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(os.Getenv(HomeEnv)); dir != "" {
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", HomeEnv, err)
		}
		return absolute, nil
	}
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
