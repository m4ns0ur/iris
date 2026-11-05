package tool

import (
	"os"
	"strings"
	"sync"
)

// captureWriter is a bounded, concurrency-safe writer for command output.
//
// It always retains the last tailMax bytes in memory, which bounds agent
// context growth even if a command produces gigabytes of output. Optionally it
// streams the first fileLimit bytes to a temp file so the full output (up to
// the capture limit) can be inspected later.
type captureWriter struct {
	mu sync.Mutex

	tail    []byte
	tailMax int
	total   int64

	file        *os.File
	fileLimit   int64
	fileWritten int64
	fileErr     error
}

type captureSnapshot struct {
	Tail          []byte
	Total         int64
	FileWritten   int64
	FileErr       error
	FileTruncated bool
}

func newCaptureWriter(tailMax int, file *os.File, fileLimit int) *captureWriter {
	if tailMax <= 0 {
		tailMax = 25 * 1024
	}
	if fileLimit <= 0 {
		fileLimit = 10 * 1024 * 1024
	}

	writer := &captureWriter{
		tailMax:   tailMax,
		tail:      make([]byte, 0, tailMax),
		file:      file,
		fileLimit: int64(fileLimit),
	}

	return writer
}

// Write implements io.Writer. It never returns an error for file write
// failures: a broken temp file must not kill the child process.
func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}

	w.total += int64(n)

	if n >= w.tailMax {
		w.tail = append(w.tail[:0], p[n-w.tailMax:]...)
	} else {
		if len(w.tail)+n > w.tailMax {
			drop := len(w.tail) + n - w.tailMax
			w.tail = append(w.tail[:0], w.tail[drop:]...)
		}
		w.tail = append(w.tail, p...)
	}

	if w.file != nil && w.fileErr == nil {
		remaining := w.fileLimit - w.fileWritten
		if remaining > 0 {
			chunk := p
			if int64(len(chunk)) > remaining {
				chunk = chunk[:remaining]
			}
			written, err := w.file.Write(chunk)
			w.fileWritten += int64(written)
			if err != nil {
				w.fileErr = err
			}
		}
	}

	return n, nil
}

func (w *captureWriter) Snapshot() captureSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()

	return captureSnapshot{
		Tail:          append([]byte(nil), w.tail...),
		Total:         w.total,
		FileWritten:   w.fileWritten,
		FileErr:       w.fileErr,
		FileTruncated: w.file != nil && w.total > w.fileWritten,
	}
}

// limitOutput bounds captured output by bytes (keeping the tail) and by line
// count. It reports whether anything was dropped.
func limitOutput(raw []byte, maxBytes, maxLines int) (string, bool) {
	truncated := false

	if maxBytes > 0 && len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
		truncated = true
	}

	lines := strings.Split(string(raw), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}

	return strings.Join(lines, "\n"), truncated
}
