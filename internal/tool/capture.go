package tool

import (
	"bytes"
	"os"
	"strings"
	"sync"
)

// captureWriter is a bounded, concurrency-safe writer for command output.
//
// It always retains the last tailMax bytes in memory, which bounds agent
// context growth even if a command produces gigabytes of output. Optionally it
// streams the first fileLimit bytes to a full-output file so the full output (up to
// the capture limit) can be inspected later.
type captureWriter struct {
	mu sync.Mutex

	tail     []byte
	tailMax  int
	total    int64
	newlines int64

	file        *os.File
	fileLimit   int64
	fileWritten int64
	fileErr     error
}

type captureSnapshot struct {
	Tail          []byte
	Total         int64
	TotalLines    int
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
// failures: a broken output file must not kill the child process.
func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}

	w.total += int64(n)
	w.newlines += int64(bytes.Count(p, []byte{'\n'}))

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

	totalLines := int(w.newlines)
	if w.total > 0 && (len(w.tail) == 0 || w.tail[len(w.tail)-1] != '\n') {
		totalLines++
	}

	return captureSnapshot{
		Tail:          append([]byte(nil), w.tail...),
		Total:         w.total,
		TotalLines:    totalLines,
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
