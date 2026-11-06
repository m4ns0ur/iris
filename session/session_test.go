package session

import (
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
