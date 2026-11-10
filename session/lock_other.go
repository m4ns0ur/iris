//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package session

import (
	"errors"
	"fmt"
	"os"
)

// acquireOSLock uses an exclusive create on platforms without flock. A crashed
// process can leave a stale lock; remove the .lock file to recover.
func acquireOSLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrSessionLocked
		}
		return nil, err
	}
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Sync()
	return file, nil
}

func releaseOSLock(path string, file *os.File) error {
	closeErr := file.Close()
	removeErr := os.Remove(path)
	if closeErr != nil {
		return closeErr
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}
