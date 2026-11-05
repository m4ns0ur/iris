package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const tempSessionPrefix = "session-"

// TempStore owns the per-process directory used for captured shell output.
// The directory is removed when the store is closed, and stale directories
// from crashed processes are removed on startup.
type TempStore struct {
	dir string

	mu     sync.Mutex
	closed bool
}

// NewTempStore creates a private session directory. ttl controls how long
// session directories from previous runs are kept before being reaped.
func NewTempStore(ttl time.Duration) (*TempStore, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	var lastErr error
	for _, base := range tempBaseDirs() {
		if err := os.MkdirAll(base, 0o700); err != nil {
			lastErr = err
			continue
		}
		// MkdirAll does not tighten permissions on an existing directory.
		_ = os.Chmod(base, 0o700)

		cleanupStaleTempDirs(base, ttl)

		dir, err := os.MkdirTemp(base, tempSessionPrefix)
		if err != nil {
			lastErr = err
			continue
		}

		return &TempStore{dir: dir}, nil
	}

	return nil, fmt.Errorf("create temp session: %w", lastErr)
}

// Dir is the session directory. The read tool is granted read access to it so
// that the model can inspect full shell output.
func (s *TempStore) Dir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// NewFile creates a 0600 temp file in the session directory.
func (s *TempStore) NewFile(pattern string) (*os.File, error) {
	if s == nil {
		return nil, errors.New("nil temp store")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("temp store is closed")
	}

	file, err := os.CreateTemp(s.dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	return file, nil
}

// Remove deletes a file created by this store.
func (s *TempStore) Remove(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// Close removes the session directory and every file in it.
func (s *TempStore) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	return os.RemoveAll(s.dir)
}

// tempBaseDirs lists candidate parent directories for shell output. The user
// cache is preferred, but some sandboxes only allow the system temp directory,
// so it is used as a fallback.
func tempBaseDirs() []string {
	dirs := make([]string, 0, 2)

	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		dirs = append(dirs, filepath.Join(cacheDir, "iris", "tmp"))
	}

	systemTemp := filepath.Join(os.TempDir(), fmt.Sprintf("iris-%d", os.Getuid()), "tmp")
	if len(dirs) == 0 || dirs[0] != systemTemp {
		dirs = append(dirs, systemTemp)
	}

	return dirs
}

func cleanupStaleTempDirs(base string, ttl time.Duration) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-ttl)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), tempSessionPrefix) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(base, entry.Name()))
		}
	}
}
