package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
)

func resetSink() {
	logSink.mu.Lock()
	defer logSink.mu.Unlock()

	if logSink.file != nil {
		_ = logSink.file.Close()
	}
	if logSink.processFile != nil {
		_ = logSink.processFile.Close()
	}
	logSink.handler = nil
	logSink.file = nil
	logSink.process = nil
	logSink.processFile = nil
	logSink.sessionID = ""
	levelVar.Set(slog.LevelDebug)
}

// newLogTest isolates the logger in a temporary log directory.
func newLogTest(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "logs")
	t.Setenv(logDirEnv, dir)
	resetSink()
	t.Cleanup(resetSink)
	return dir
}

func readMessages(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file %q: %v", path, err)
	}

	var messages []string
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		message, _ := record["msg"].(string)
		messages = append(messages, message)
	}
	return messages
}

func assertMessages(t *testing.T, path string, want ...string) {
	t.Helper()

	got := readMessages(t, path)
	if !slices.Equal(got, want) {
		t.Fatalf("messages in %q = %v, want %v", path, got, want)
	}
}

func TestLogsAreSplitPerSession(t *testing.T) {
	dir := newLogTest(t)

	Logger.Info("process-before")
	SetSession("s-one")
	Logger.Info("session-one")
	SetSession("s-two")
	Logger.Info("session-two")
	ClearSession("s-two")
	Logger.Info("process-after")

	assertMessages(t, filepath.Join(dir, processLogFile), "process-before", "process-after")
	assertMessages(t, filepath.Join(dir, "s-one.log"), "session-one")
	assertMessages(t, filepath.Join(dir, "s-two.log"), "session-two")
}

func TestResumedSessionAppendsToItsLog(t *testing.T) {
	dir := newLogTest(t)

	SetSession("s-resume")
	Logger.Info("first")
	ClearSession("s-resume")

	SetSession("s-resume")
	Logger.Info("second")

	assertMessages(t, filepath.Join(dir, "s-resume.log"), "first", "second")
}

func TestClearSessionIgnoresOtherSessions(t *testing.T) {
	dir := newLogTest(t)

	SetSession("s-one")
	ClearSession("s-two")
	Logger.Info("still-one")

	ClearSession("s-one")
	Logger.Info("process")

	assertMessages(t, filepath.Join(dir, "s-one.log"), "still-one")
	assertMessages(t, filepath.Join(dir, processLogFile), "process")
}

func TestSessionLogPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX permission bits")
	}

	dir := filepath.Join(t.TempDir(), "nested", "logs")
	t.Setenv(logDirEnv, dir)
	resetSink()
	t.Cleanup(resetSink)

	SetSession("s-perm")
	Logger.Info("hello")

	fileInfo, err := os.Stat(filepath.Join(dir, "s-perm.log"))
	if err != nil {
		t.Fatalf("stat session log: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("session log permissions = %o, want 600", perm)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat log directory: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("log directory permissions = %o, want 700", perm)
	}
}

func TestInvalidSessionIDIsRejected(t *testing.T) {
	dir := newLogTest(t)

	SetSession("../escape")
	Logger.Info("process")

	messages := readMessages(t, filepath.Join(dir, processLogFile))
	if !slices.Contains(messages, "process") {
		t.Fatalf("process log messages = %v, want process entry", messages)
	}
	if !slices.Contains(messages, "ignoring invalid session id for log file") {
		t.Fatalf("process log messages = %v, want invalid-id warning", messages)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.log")); err == nil {
		t.Fatalf("invalid session id created a log outside the log directory")
	}
}

func TestAttrsSurviveSessionSwitch(t *testing.T) {
	dir := newLogTest(t)

	SetSession("s-attrs")
	Logger.With("component", "test").WithGroup("group").Info("attributed", "answer", 42)

	data, err := os.ReadFile(filepath.Join(dir, "s-attrs.log"))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	var record struct {
		Component string `json:"component"`
		Group     struct {
			Answer int `json:"answer"`
		} `json:"group"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("decode session log: %v", err)
	}
	if record.Component != "test" {
		t.Fatalf("component = %q, want test", record.Component)
	}
	if record.Group.Answer != 42 {
		t.Fatalf("group.answer = %d, want 42", record.Group.Answer)
	}
}

func TestConcurrentLoggingAndSessionSwitching(t *testing.T) {
	dir := newLogTest(t)

	SetSession("s-a")
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				Logger.Info("concurrent", "worker", worker)
			}
		}(worker)
	}
	for i := 0; i < 100; i++ {
		SetSession("s-a")
		SetSession("s-b")
	}
	wg.Wait()
	ClearSession("")
	Logger.Info("finished")

	messages := readMessages(t, filepath.Join(dir, processLogFile))
	if !slices.Contains(messages, "finished") {
		t.Fatalf("process log messages = %v, want finished entry", messages)
	}
}
