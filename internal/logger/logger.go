package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/m4ns0ur/iris/internal/agentdir"
)

const (
	// processLogFile receives messages written outside an active session,
	// such as startup, configuration, and shutdown logs.
	processLogFile = "iris.log"

	// sessionLogExt is appended to the session ID for its log file.
	sessionLogExt = ".log"

	// logDirEnv overrides the default log directory. It is primarily useful
	// for tests and read-only home directories.
	logDirEnv = "IRIS_LOG_DIR"
)

var (
	Logger   *slog.Logger
	levelVar = new(slog.LevelVar)
	logSink  = new(sink)
)

type LLMMessage struct {
	Role    any `json:"role"`
	Content any `json:"content"`
}

type LLMChoice struct {
	Message any `json:"message"`
}

func init() {
	levelVar.Set(slog.LevelDebug)
	Logger = slog.New(&sessionHandler{sink: logSink})
}

// SetSession routes subsequent log records to the active session's own log
// file, appending to it if the session is resumed. Records written before a
// session is active, and after ClearSession, go to the process log.
func SetSession(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		ClearSession("")
		return
	}
	if !validSessionID(id) {
		Logger.Warn("ignoring invalid session id for log file", "session_id", id)
		return
	}

	logSink.mu.Lock()
	if logSink.sessionID == id {
		logSink.mu.Unlock()
		return
	}

	file, err := openLogFile(id + sessionLogExt)
	if err != nil {
		logSink.useProcessLocked()
		logSink.logLocked(
			slog.LevelWarn,
			"cannot open session log; using the process log instead",
			"session_id", id,
			"error", err,
		)
		logSink.mu.Unlock()
		return
	}
	logSink.swapLocked(newFileHandler(file), file, id)
	logSink.mu.Unlock()
}

// ClearSession detaches the session log when id matches the active session, or
// unconditionally when id is empty. Records written afterwards go to the
// process log.
func ClearSession(id string) {
	logSink.mu.Lock()
	defer logSink.mu.Unlock()

	if id != "" && logSink.sessionID != id {
		return
	}
	if logSink.handler == nil && logSink.file == nil {
		return
	}
	logSink.useProcessLocked()
}

func SetDebug(enable bool) {
	if enable {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
	Logger.Debug("Debug mode enabled")
}

// sink owns the currently active handler. All handlers derived from Logger
// share one sink, so switching sessions also affects loggers retained by
// packages as package-level variables.
type sink struct {
	mu          sync.Mutex
	handler     slog.Handler
	file        *os.File
	process     slog.Handler
	processFile *os.File
	sessionID   string
}

// swapLocked installs the next handler and closes the previous session file.
// The caller must hold s.mu.
func (s *sink) swapLocked(next slog.Handler, file *os.File, id string) {
	previous := s.file
	s.handler = next
	s.file = file
	s.sessionID = id
	if previous != nil && previous != file {
		_ = previous.Close()
	}
}

// useProcessLocked switches to the process log handler, opening it on first
// use. The caller must hold s.mu.
func (s *sink) useProcessLocked() {
	if s.process == nil {
		file, err := openLogFile(processLogFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "iris: warning: cannot write process log: %v\n", err)
			s.process = newStderrHandler()
		} else {
			s.processFile = file
			s.process = newFileHandler(file)
		}
	}
	s.swapLocked(s.process, nil, "")
}

// logLocked writes a record through the current handler while the caller holds
// s.mu. It is used for logger-internal warnings so they cannot deadlock on a
// second sink lock.
func (s *sink) logLocked(level slog.Level, message string, args ...any) {
	if s.handler == nil {
		s.useProcessLocked()
	}
	record := slog.NewRecord(time.Now(), level, message, 0)
	record.Add(args...)
	_ = s.handler.Handle(context.Background(), record)
}

// sessionHandler is stable for the process lifetime. It resolves the active
// file handler on every record, which lets the logger follow session switches
// without replacing logger.Logger (and therefore without breaking the copies
// other packages hold).
type sessionHandler struct {
	sink     *sink
	decorate func(slog.Handler) slog.Handler
}

func (h *sessionHandler) Enabled(_ context.Context, level slog.Level) bool {
	return levelVar.Level() <= level
}

func (h *sessionHandler) Handle(ctx context.Context, record slog.Record) error {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()

	if h.sink.handler == nil {
		h.sink.useProcessLocked()
	}
	handler := h.sink.handler
	if h.decorate != nil {
		handler = h.decorate(handler)
	}
	return handler.Handle(ctx, record)
}

func (h *sessionHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	attrs = append([]slog.Attr(nil), attrs...)
	return &sessionHandler{
		sink: h.sink,
		decorate: func(base slog.Handler) slog.Handler {
			if h.decorate != nil {
				base = h.decorate(base)
			}
			return base.WithAttrs(attrs)
		},
	}
}

func (h *sessionHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &sessionHandler{
		sink: h.sink,
		decorate: func(base slog.Handler) slog.Handler {
			if h.decorate != nil {
				base = h.decorate(base)
			}
			return base.WithGroup(name)
		},
	}
}

func logDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(logDirEnv)); dir != "" {
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", logDirEnv, err)
		}
		return absolute, nil
	}
	return agentdir.Logs()
}

func openLogFile(name string) (*os.File, error) {
	dir, err := logDir()
	if err != nil {
		return nil, fmt.Errorf("resolve log directory: %w", err)
	}
	if err := agentdir.Ensure(dir); err != nil {
		return nil, fmt.Errorf("create log directory %q: %w", dir, err)
	}

	path := filepath.Join(dir, name)
	// Logs include prompts and tool output, so keep them owner-only.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	_ = file.Chmod(0o600)
	return file, nil
}

func newFileHandler(file *os.File) slog.Handler {
	return slog.NewJSONHandler(file, &slog.HandlerOptions{Level: levelVar})
}

func newStderrHandler() slog.Handler {
	return slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: levelVar})
}

// validSessionID mirrors the session store rule. Log file names are built from
// the ID, so separators and traversal sequences must be rejected.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}
