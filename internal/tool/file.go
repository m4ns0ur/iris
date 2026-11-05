package tool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename. This prevents partially written files when the
// process is interrupted and ensures edits are all-or-nothing.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)

	file, err := os.CreateTemp(dir, ".iris-write-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}

	tempPath := file.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}

	// Keep the temp file private while it is being written, then apply the
	// final mode immediately before publishing it with the rename.
	if perm == 0 {
		perm = 0o644
	}
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}

	// Best-effort directory sync so the rename survives a crash.
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}

	return nil
}

// existingFileMode returns the permission bits of an existing regular file.
// It reports whether the file exists and rejects directories, devices, FIFOs
// and other special files.
func existingFileMode(path string) (fs.FileMode, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}

	if !info.Mode().IsRegular() {
		return 0, true, fmt.Errorf("%q is not a regular file", path)
	}

	return info.Mode().Perm(), true, nil
}
