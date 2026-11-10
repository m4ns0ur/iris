package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// processLocks prevents two managers in the same process from holding the same
// lease. OS file locks cover separate processes; this map covers the case where
// flock/O_EXCL semantics are platform-dependent.
var processLocks sync.Map // absolute lock path -> struct{}

// Lease is an exclusive writer lease for one session. It remains held until
// Release or process exit. A crashed process releases its OS lock automatically.
//
// Managers may return their own Lease implementation; the client and TUI only
// require Release.
type Lease interface {
	// Release relinquishes the writer lease. It must be safe to call more than
	// once.
	Release() error
}

// fileLease is the JSONL manager's Lease implementation.
type fileLease struct {
	manager *JSONLManager
	id      string
	path    string
	file    *os.File
	once    sync.Once
	err     error
}

// Release relinquishes the writer lease. It is safe to call more than once.
func (l *fileLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.manager == nil {
			l.err = releaseLeaseFile(l.path, l.file)
			return
		}

		// Hold the manager lock so an in-flight append/save finishes before
		// the OS lease becomes available to another process.
		l.manager.mu.Lock()
		defer l.manager.mu.Unlock()
		if current := l.manager.leases[l.id]; current == l {
			delete(l.manager.leases, l.id)
		}
		l.err = releaseLeaseFile(l.path, l.file)
	})
	return l.err
}

func acquireLeaseFile(path string) (*os.File, error) {
	if _, loaded := processLocks.LoadOrStore(path, struct{}{}); loaded {
		return nil, fmt.Errorf("%w", ErrSessionLocked)
	}

	file, err := acquireOSLock(path)
	if err != nil {
		processLocks.Delete(path)
		return nil, err
	}
	return file, nil
}

func releaseLeaseFile(path string, file *os.File) error {
	processLocks.Delete(path)
	if file == nil {
		return nil
	}
	return releaseOSLock(path, file)
}

func (m *JSONLManager) lockPath(id string) string {
	return filepath.Join(m.dir, id+".lock")
}
