package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

func newTestManager(t *testing.T) *JSONLManager {
	t.Helper()

	manager, err := NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	return manager
}

func TestJSONLSessionRoundTrip(t *testing.T) {
	manager := newTestManager(t)

	session, err := manager.Create(CreateOptions{
		Model:     "test-model",
		Workspace: "/tmp/workspace",
		ModelMetadata: map[string]string{
			"provider": "openai-compatible",
			"context":  "75000",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	user := session.AddUser("hello")
	assistant := session.AddAssistant(
		"calling a tool",
		"thinking",
		[]provider.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: provider.FunctionCall{
				Name:      "read",
				Arguments: `{"path":"a.txt"}`,
			},
		}},
		&provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		"test-model",
	)
	tool := session.AddTool("call-1", "read", `{"content":"data"}`)
	final := session.AddAssistant(
		"done",
		"",
		nil,
		&provider.Usage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23},
		"test-model",
	)

	if user.ParentID != "" {
		t.Fatalf("first entry parent = %q, want empty", user.ParentID)
	}
	if assistant.ParentID != user.ID || tool.ParentID != assistant.ID || final.ParentID != tool.ID {
		t.Fatalf("parent links = %q, %q, %q", assistant.ParentID, tool.ParentID, final.ParentID)
	}

	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if loaded.ID != session.ID || loaded.Model != "test-model" {
		t.Fatalf("loaded metadata = %+v", loaded.Metadata)
	}
	if loaded.ModelMetadata["provider"] != "openai-compatible" {
		t.Fatalf("model metadata = %v", loaded.ModelMetadata)
	}
	if len(loaded.Entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(loaded.Entries))
	}
	if loaded.Usage.TotalTokens != 35 {
		t.Fatalf("usage = %+v, want 35 total tokens", loaded.Usage)
	}

	history := loaded.History()
	if len(history) != 4 {
		t.Fatalf("history messages = %d, want 4", len(history))
	}
	if history[0].Role != "user" || history[1].Role != "assistant" || history[2].Role != "tool" {
		t.Fatalf("history roles = %q, %q, %q", history[0].Role, history[1].Role, history[2].Role)
	}
	if len(history[1].ToolCalls) != 1 || history[1].ToolCalls[0].Function.Name != "read" {
		t.Fatalf("tool calls = %+v", history[1].ToolCalls)
	}
	if history[2].ToolCallID != "call-1" {
		t.Fatalf("tool call id = %q, want call-1", history[2].ToolCallID)
	}

	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("JSONL lines = %d, want 5", len(lines))
	}
	var header record
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Type != recordSession || header.Session == nil || header.Session.ID != session.ID {
		t.Fatalf("header = %+v", header)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestCreateDoesNotPersistUntilSaved(t *testing.T) {
	manager := newTestManager(t)

	session, err := manager.Create(CreateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("sessions = %v, want none before Save", summaries)
	}

	session.AddUser("hello")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := manager.Latest(); err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestListOrdersByUpdatedAt(t *testing.T) {
	manager := newTestManager(t)

	first, _ := manager.Create(CreateOptions{Model: "first"})
	first.AddUser("one")
	if err := manager.Save(first); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}

	time.Sleep(2 * time.Millisecond)

	second, _ := manager.Create(CreateOptions{Model: "second"})
	second.AddUser("two")
	second.AddUser("three")
	if err := manager.Save(second); err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}

	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(summaries))
	}
	if summaries[0].ID != second.ID {
		t.Fatalf("latest ID = %q, want %q", summaries[0].ID, second.ID)
	}
	if summaries[0].EntryCount != 2 {
		t.Fatalf("entry count = %d, want 2", summaries[0].EntryCount)
	}

	latest, err := manager.Latest()
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("Latest() = %q, want %q", latest.ID, second.ID)
	}
}

func TestForkCopiesThroughUserMessage(t *testing.T) {
	manager := newTestManager(t)

	parent, _ := manager.Create(CreateOptions{Model: "m", Workspace: "/w"})
	parent.AddUser("first")
	parent.AddAssistant("answer one", "", nil, nil, "m")
	secondUser := parent.AddUser("second")
	parent.AddAssistant("answer two", "", nil, nil, "m")
	if err := manager.Save(parent); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	child, err := manager.Fork(parent.ID, secondUser.ID)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if child.ID == parent.ID {
		t.Fatal("fork reused parent ID")
	}
	if child.ParentSessionID != parent.ID || child.ForkedFromEntryID != secondUser.ID {
		t.Fatalf("fork metadata = %+v", child.Metadata)
	}
	if len(child.Entries) != 3 {
		t.Fatalf("fork entries = %d, want 3", len(child.Entries))
	}
	for _, entry := range child.Entries {
		if entry.SessionID != child.ID {
			t.Fatalf("entry session = %q, want %q", entry.SessionID, child.ID)
		}
	}
	if child.Entries[2].Content != "second" {
		t.Fatalf("last fork entry = %q, want second", child.Entries[2].Content)
	}

	loaded, err := manager.Open(child.ID)
	if err != nil {
		t.Fatalf("Open(fork) error = %v", err)
	}
	if len(loaded.History()) != 3 {
		t.Fatalf("fork history = %d, want 3", len(loaded.History()))
	}

	if _, err := manager.Fork(parent.ID, parent.Entries[1].ID); !errors.Is(err, ErrInvalidForkPoint) {
		t.Fatalf("Fork(assistant) error = %v, want ErrInvalidForkPoint", err)
	}
}

func TestExportMarkdown(t *testing.T) {
	manager := newTestManager(t)

	session, _ := manager.Create(CreateOptions{Model: "test-model"})
	session.AddUser("hello")
	session.AddAssistant("hi", "thinking", nil, &provider.Usage{TotalTokens: 3}, "test-model")
	session.AddTool("call-1", "read", "tool output")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path, err := manager.ExportMarkdown(session)
	if err != nil {
		t.Fatalf("ExportMarkdown() error = %v", err)
	}
	if !strings.HasSuffix(path, session.ID+".md") {
		t.Fatalf("export path = %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(export) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# Iris Session " + session.ID,
		"- Model: test-model",
		"## User",
		"hello",
		"## Assistant",
		"hi",
		"### Tool result: read (call-1)",
		"tool output",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing %q:\n%s", want, text)
		}
	}
}

func TestInvalidSessionIDIsRejected(t *testing.T) {
	manager := newTestManager(t)

	for _, id := range []string{"", "../escape", "a/b", "a.b"} {
		if _, err := manager.Open(id); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("Open(%q) error = %v, want ErrInvalidSessionID", id, err)
		}
	}
}

func TestUnknownSession(t *testing.T) {
	manager := newTestManager(t)

	if _, err := manager.Open("s-missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Open() error = %v, want ErrSessionNotFound", err)
	}
	if _, err := manager.Latest(); !errors.Is(err, ErrNoSessions) {
		t.Fatalf("Latest() error = %v, want ErrNoSessions", err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(append) error = %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("WriteString(append) error = %v", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("Sync(append) error = %v", err)
	}
}

func TestSaveWritesSchemaVersion(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	session.AddUser("hello")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(manager.Dir(), session.ID+".jsonl"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var header record
	if err := json.Unmarshal(bytesLine(data), &header); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if header.Version != FormatVersion {
		t.Fatalf("version = %d, want %d", header.Version, FormatVersion)
	}
	if header.Session == nil || header.Session.Revision != 1 {
		t.Fatalf("header revision = %+v", header.Session)
	}
	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !loaded.UpdatedAt.Equal(header.Session.UpdatedAt) {
		t.Fatalf("loaded updated_at = %s, header updated_at = %s", loaded.UpdatedAt, header.Session.UpdatedAt)
	}
}

func bytesLine(data []byte) []byte {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	return line
}

func TestAppendJournalAndPartialTailRecovery(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "hello"}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{
		Kind:    EntryAssistant,
		Content: "hi",
		Usage:   &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		Model:   "m",
	}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}
	if session.Revision != 2 {
		t.Fatalf("revision = %d, want 2", session.Revision)
	}

	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("journal lines = %d, want header + 2 entries + checkpoint", len(lines))
	}
	var checkpointRecord record
	if err := json.Unmarshal([]byte(lines[3]), &checkpointRecord); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	if checkpointRecord.Type != recordCheckpoint || checkpointRecord.Checkpoint == nil || checkpointRecord.Checkpoint.Revision != 2 {
		t.Fatalf("checkpoint = %+v", checkpointRecord.Checkpoint)
	}

	appendFile(t, path, `{"type":"entry","entry":{`)

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() after partial tail error = %v", err)
	}
	if !loaded.Recovered {
		t.Fatal("Open() did not mark the partial tail as recovered")
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want 12", loaded.Usage)
	}
	if !loaded.UpdatedAt.Equal(loaded.Entries[1].CreatedAt) {
		t.Fatalf("updated_at = %s, want checkpoint time %s", loaded.UpdatedAt, loaded.Entries[1].CreatedAt)
	}

	lease, err = manager.Acquire(loaded)
	if err != nil {
		t.Fatalf("Acquire(recovered) error = %v", err)
	}
	if loaded.needsRepair {
		t.Fatal("Acquire did not clear needsRepair")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release(recovered) error = %v", err)
	}

	reopened, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open(repaired) error = %v", err)
	}
	if reopened.Recovered {
		t.Fatal("repaired session still reports recovery")
	}
	if len(reopened.Entries) != 2 {
		t.Fatalf("repaired entries = %d, want 2", len(reopened.Entries))
	}
}

func TestInterruptedToolExecutionRecovered(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "read it"}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{
		Kind: EntryAssistant,
		ToolCalls: []provider.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: provider.FunctionCall{
				Name:      "read",
				Arguments: `{"path":"a.txt"}`,
			},
		}},
	}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}

	// Simulate a crash after the assistant tool call was durably written but
	// before any tool result was recorded.
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !loaded.Recovered {
		t.Fatal("Open() did not identify the interrupted tool execution")
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries before repair = %d, want user + assistant", len(loaded.Entries))
	}

	history := loaded.History()
	if len(history) != 3 || history[2].Role != "tool" || history[2].ToolCallID != "call_1" {
		t.Fatalf("history = %+v", history)
	}
	if !strings.Contains(history[2].Content.(string), "tool_execution_interrupted") {
		t.Fatalf("synthesized history content = %q", history[2].Content)
	}

	lease, err = manager.Acquire(loaded)
	if err != nil {
		t.Fatalf("Acquire(recovered) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release(recovered) error = %v", err)
	}

	reopened, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open(repaired) error = %v", err)
	}
	if len(reopened.Entries) != 3 || reopened.Entries[2].Status != EntryStatusInterrupted || !reopened.Entries[2].Recovered {
		t.Fatalf("persisted entries = %+v", reopened.Entries)
	}
	path, err := manager.ExportMarkdown(reopened)
	if err != nil {
		t.Fatalf("ExportMarkdown() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(export) error = %v", err)
	}
	if !strings.Contains(string(data), "[interrupted]") {
		t.Fatalf("export does not identify the interrupted tool:\n%s", data)
	}
}

func TestRecoveryOnlyReplacesMissingToolResults(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "go"}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{
		Kind: EntryAssistant,
		ToolCalls: []provider.ToolCall{
			{ID: "call_1", Type: "function", Function: provider.FunctionCall{Name: "read"}},
			{ID: "call_2", Type: "function", Function: provider.FunctionCall{Name: "read"}},
		},
	}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{Kind: EntryTool, ToolCallID: "call_1", ToolName: "read", Content: `{"ok":true}`}); err != nil {
		t.Fatalf("Append(tool) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(loaded.Entries) != 3 {
		t.Fatalf("entries before repair = %d, want user + assistant + existing tool", len(loaded.Entries))
	}
	if loaded.Entries[2].ToolCallID != "call_1" || loaded.Entries[2].Status != "" {
		t.Fatalf("existing result changed: %+v", loaded.Entries[2])
	}
	history := loaded.History()
	if len(history) != 4 || history[3].ToolCallID != "call_2" {
		t.Fatalf("history after recovery = %+v", history)
	}
	if !strings.Contains(history[3].Content.(string), "tool_execution_interrupted") {
		t.Fatalf("missing synthetic result = %+v", history[3])
	}
}

func TestConcurrentWriterLease(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	other, err := NewJSONL(manager.Dir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	loaded, err := other.Open(session.ID)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Open() error = %v", err)
	}
	if loaded == nil {
		loaded = session
	}
	if _, err := other.Acquire(loaded); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("second Acquire() error = %v, want ErrSessionLocked", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	lease, err = other.Acquire(loaded)
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
}

func TestSaveRejectsStaleRevision(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	session.AddUser("one")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	first, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	second, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}

	first.AddUser("two")
	if err := manager.Save(first); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}

	second.AddUser("conflict")
	if err := manager.Save(second); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("Save(second) error = %v, want ErrSessionConflict", err)
	}
}

func TestUnsupportedSessionVersionRejected(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	header := record{
		Type:    recordSession,
		Version: FormatVersion + 1,
		Session: &session.Metadata,
	}
	data, _ := json.Marshal(header)
	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := manager.Open(session.ID); !errors.Is(err, ErrUnsupportedSessionVersion) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedSessionVersion", err)
	}
}

func TestLegacySessionIsUpgradedOnAcquire(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	legacy := struct {
		Type    string    `json:"type"`
		Session *Metadata `json:"session"`
	}{Type: recordSession, Session: &session.Metadata}
	headerData, _ := json.Marshal(legacy)
	entry := session.AddUser("legacy hello")
	entryData, _ := json.Marshal(record{Type: recordEntry, Entry: &entry})
	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	if err := os.WriteFile(path, append(append(headerData, '\n'), append(entryData, '\n')...), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open(legacy) error = %v", err)
	}
	if loaded.Recovered {
		t.Fatal("clean legacy session should not be marked recovered")
	}
	if !loaded.needsRepair {
		t.Fatal("legacy session was not marked for format upgrade")
	}
	lease, err := manager.Acquire(loaded)
	if err != nil {
		t.Fatalf("Acquire(legacy) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var header record
	if err := json.Unmarshal(bytesLine(data), &header); err != nil {
		t.Fatalf("decode upgraded header: %v", err)
	}
	if header.Version != FormatVersion {
		t.Fatalf("upgraded version = %d, want %d", header.Version, FormatVersion)
	}
}

func TestAppendRequiresLease(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "hello"}); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("Append() without lease error = %v, want ErrSessionLocked", err)
	}
}

func TestHistoryOmitsOrphanToolResults(t *testing.T) {
	session, _ := NewJSONL(t.TempDir())
	created, _ := session.Create(CreateOptions{Model: "m"})
	created.AddUser("hello")
	created.AddTool("missing-call", "read", "orphan")
	created.AddAssistant("done", "", nil, nil, "m")
	history := created.History()
	if len(history) != 2 {
		t.Fatalf("history = %+v, want user + assistant", history)
	}
}

func TestListToleratesPartialTail(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	session.AddUser("hello")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	appendFile(t, filepath.Join(manager.Dir(), session.ID+".jsonl"), `{"type":"entry"`)

	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != session.ID || summaries[0].EntryCount != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
}

func TestPartialCheckpointRecovery(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "hello"}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{
		Kind:    EntryAssistant,
		Content: "hi",
		Usage:   &provider.Usage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5},
	}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}
	appendFile(t, filepath.Join(manager.Dir(), session.ID+".jsonl"), `{"type":"checkpoint","checkpoint":{`)
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !loaded.Recovered || len(loaded.Entries) != 2 {
		t.Fatalf("loaded = recovered %v, entries %d", loaded.Recovered, len(loaded.Entries))
	}
	if loaded.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want 5", loaded.Usage)
	}

	lease, err = manager.Acquire(loaded)
	if err != nil {
		t.Fatalf("Acquire(repaired) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release(repaired) error = %v", err)
	}
}

func TestTruncatedButValidFinalRecordIsRecovered(t *testing.T) {
	for _, partial := range []string{
		"{\"type\":\"entry\"}\n",
		"{\"type\":\"checkpoint\"}\n",
	} {
		t.Run(partial, func(t *testing.T) {
			manager := newTestManager(t)
			session, _ := manager.Create(CreateOptions{Model: "m"})
			session.AddUser("hello")
			if err := manager.Save(session); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			appendFile(t, filepath.Join(manager.Dir(), session.ID+".jsonl"), partial)

			loaded, err := manager.Open(session.ID)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if !loaded.Recovered || len(loaded.Entries) != 1 {
				t.Fatalf("loaded = recovered %v, entries %d", loaded.Recovered, len(loaded.Entries))
			}
			lease, err := manager.Acquire(loaded)
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			if err := lease.Release(); err != nil {
				t.Fatalf("Release() error = %v", err)
			}
		})
	}
}

func TestFileWithoutHeaderIsNotRecreated(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"entry","entry":{"kind":"user","content":"orphan"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := manager.Open(session.ID); err == nil {
		t.Fatal("Open() accepted a file without a header")
	}
	if _, err := manager.Acquire(session); err == nil {
		t.Fatal("Acquire() treated a corrupt file as a new session")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() after cleanup error = %v, want released lock", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestSaveUsesTransientLease(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	session.AddUser("hello")
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	other, err := NewJSONL(manager.Dir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	if err := other.Save(session); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("Save() while leased error = %v, want ErrSessionLocked", err)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := other.Save(session); err != nil {
		t.Fatalf("Save() after release error = %v", err)
	}
}

func TestAppendRepairsPartialTailInProcess(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})
	lease, err := manager.Acquire(session)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "one"}); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := manager.Append(session, Entry{Kind: EntryAssistant, Content: "two"}); err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}

	path := filepath.Join(manager.Dir(), session.ID+".jsonl")
	appendFile(t, path, `{"type":"entry","entry":{`)

	if _, err := manager.Append(session, Entry{Kind: EntryUser, Content: "three"}); err != nil {
		t.Fatalf("Append(after partial tail) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	loaded, err := manager.Open(session.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if loaded.Recovered {
		t.Fatal("journal still reports recovery after an in-process tail repair")
	}
	if len(loaded.Entries) != 3 || loaded.Entries[2].Content != "three" {
		t.Fatalf("entries = %+v", loaded.Entries)
	}
}

func TestHistoryPlacesSyntheticResultBeforeNextMessage(t *testing.T) {
	manager := newTestManager(t)
	created, _ := manager.Create(CreateOptions{Model: "m"})
	created.AddUser("first")
	created.AddAssistant("", "", []provider.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Function: provider.FunctionCall{
			Name: "read",
		},
	}}, nil, "m")
	created.AddUser("second")

	history := created.History()
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4: %+v", len(history), history)
	}
	if history[2].Role != "tool" || history[2].ToolCallID != "call_1" {
		t.Fatalf("synthetic result position = %+v", history[2])
	}
	if history[3].Role != "user" {
		t.Fatalf("following message = %+v", history[3])
	}
	if len(created.Entries) != 3 {
		t.Fatalf("transcript was mutated: %+v", created.Entries)
	}
}

func TestToolOutputDirectory(t *testing.T) {
	manager := newTestManager(t)
	session, _ := manager.Create(CreateOptions{Model: "m"})

	dir, err := manager.ToolOutputDir(session.ID)
	if err != nil {
		t.Fatalf("ToolOutputDir() error = %v", err)
	}
	if filepath.Dir(dir) != manager.Dir() || filepath.Base(dir) != "tool-output" {
		t.Fatalf("tool output dir = %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("tool output dir mode = %v, want 0700", info.Mode().Perm())
	}

	other, err := manager.ToolOutputDir("s-another")
	if err != nil {
		t.Fatalf("ToolOutputDir(other) error = %v", err)
	}
	if other != dir {
		t.Fatalf("tool output dirs differ: %q vs %q", dir, other)
	}

	if _, err := manager.ToolOutputDir("../escape"); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("invalid tool output session error = %v, want ErrInvalidSessionID", err)
	}

	session.AddUser("hello")
	if err := manager.Save(session); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("tool-output directory was listed as a session: %+v", summaries)
	}
}
