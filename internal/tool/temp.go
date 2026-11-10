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

// TempStore owns the directory used for full tool output. It starts as a
// per-process temporary directory and can be redirected to a persistent
// session-scoped directory. Ephemeral stores are removed on Close; persistent
// stores keep their files for later retrieval. Stale ephemeral directories from
// crashed processes are removed on startup.
type TempStore struct {
	dir        string
	base       string
	persistent bool

	mu     sync.Mutex
	closed bool
}

// NewTempStore creates a private ephemeral tool-output directory. ttl controls how long
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

		return &TempStore{dir: dir, base: base}, nil
	}

	return nil, fmt.Errorf("create temp session: %w", lastErr)
}

// Dir is the tool-output directory. The read tool is granted read access to it
// so that the model can inspect full tool output.
func (s *TempStore) Dir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// SetDir redirects the store to a persistent directory. Files already in an
// ephemeral store directory are not migrated; callers should set the directory
// before running tools. The old ephemeral directory is removed.
func (s *TempStore) SetDir(dir string) error {
	if s == nil {
		return errors.New("nil temp store")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("tool output directory is empty")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve tool output directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return fmt.Errorf("create tool output directory: %w", err)
	}
	_ = os.Chmod(absolute, 0o700)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("temp store is closed")
	}

	old := s.dir
	wasPersistent := s.persistent
	s.dir = absolute
	s.persistent = true

	if !wasPersistent && old != "" && old != absolute && s.base != "" &&
		strings.HasPrefix(old, s.base+string(os.PathSeparator)) {
		// Only remove an empty ephemeral directory. If tools already produced
		// output, keep the files so paths in the transcript remain readable.
		if entries, err := os.ReadDir(old); err == nil && len(entries) == 0 {
			_ = os.Remove(old)
		}
	}
	return nil
}

// Persistent reports whether the store keeps its files after Close.
func (s *TempStore) Persistent() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistent
}

// NewFile creates a 0600 output file in the tool-output directory.
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

// Close removes an ephemeral store's directory and every file in it. A
// persistent session store is left intact so the full output remains available
// after the run and after the process exits.
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

	if s.persistent {
		return nil
	}
	return os.RemoveAll(s.dir)
}

// SaveText stores full tool output and reports how much was retained. The
// returned path is 0600 and readable by the read tool through the store's read
// root. maxBytes bounds the stored file; zero stores all data.
func (s *TempStore) SaveText(prefix string, data []byte, maxBytes int) (string, int64, bool, error) {
	if s == nil {
		return "", 0, false, errors.New("nil temp store")
	}
	prefix = outputFilePrefix(prefix)
	file, err := s.NewFile(prefix + "-*.log")
	if err != nil {
		return "", 0, false, err
	}
	path := file.Name()

	limit := len(data)
	truncated := false
	if maxBytes > 0 && limit > maxBytes {
		limit = maxBytes
		truncated = true
	}

	written, writeErr := file.Write(data[:limit])
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		s.Remove(path)
		return "", 0, false, fmt.Errorf("write tool output: %w", writeErr)
	}
	return path, int64(written), truncated, nil
}

func outputFilePrefix(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '-', char == '_':
			builder.WriteRune(char)
		}
		if builder.Len() >= 32 {
			break
		}
	}
	if builder.Len() == 0 {
		return "tool-output"
	}
	return builder.String()
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
