package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
)

type fakeAgent struct {
	mu       sync.Mutex
	history  []provider.Message
	handlers []func(agent.Event)
	approver agent.Approver
	block    chan struct{}
}

func (f *fakeAgent) Run(ctx context.Context, input string) error {
	f.mu.Lock()
	f.history = append(f.history, provider.Message{
		Role:    "user",
		Content: []provider.Content{{Type: "text", Text: input}},
	})
	f.mu.Unlock()

	f.emit(agent.AgentStarted{At: time.Now(), Input: input})
	if f.block != nil {
		select {
		case <-ctx.Done():
			f.emit(agent.AgentFinished{At: time.Now(), Canceled: true})
			return ctx.Err()
		case <-f.block:
		}
	}
	f.emit(agent.AssistantDelta{At: time.Now(), Text: "answer"})
	f.emit(agent.Usage{At: time.Now(), PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})

	f.mu.Lock()
	f.history = append(f.history, provider.Message{Role: "assistant", Content: "answer"})
	f.mu.Unlock()

	f.emit(agent.AgentFinished{At: time.Now()})
	return nil
}

func (f *fakeAgent) emit(event agent.Event) {
	f.mu.Lock()
	handlers := append([]func(agent.Event){}, f.handlers...)
	f.mu.Unlock()
	for _, handler := range handlers {
		handler(event)
	}
}

func (f *fakeAgent) Subscribe(handler func(agent.Event)) {
	f.mu.Lock()
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()
}

func (f *fakeAgent) History() []provider.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.Message(nil), f.history...)
}

func (f *fakeAgent) LoadHistory(messages []provider.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history = append([]provider.Message(nil), messages...)
	return nil
}

func (f *fakeAgent) Cancel()                             {}
func (f *fakeAgent) Close() error                        { return nil }
func (f *fakeAgent) SetApprover(approver agent.Approver) { f.approver = approver }
func (f *fakeAgent) Approver() agent.Approver            { return f.approver }
func (f *fakeAgent) ApprovalTimeout() time.Duration      { return time.Minute }

func newTestClient(t *testing.T) (*Client, *fakeAgent, *session.JSONLManager) {
	t.Helper()

	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	fake := &fakeAgent{}
	c, err := New(Options{
		Agent:    fake,
		Sessions: manager,
		SessionDefaults: session.CreateOptions{
			Model:     "test-model",
			Workspace: "/workspace",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c, fake, manager
}

func TestClientRunPersistsSessionAndReturnsResult(t *testing.T) {
	c, _, manager := newTestClient(t)

	var eventTypes []agent.EventType
	result, err := c.RunStream(context.Background(), "hello", func(event agent.Event) {
		eventTypes = append(eventTypes, event.Type())
	})
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	if result.Content != "answer" {
		t.Fatalf("content = %q, want answer", result.Content)
	}
	if result.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.SessionID == "" {
		t.Fatal("result has no session ID")
	}
	if !containsEvent(eventTypes, agent.EventAssistantDelta) || !containsEvent(eventTypes, agent.EventAgentFinished) {
		t.Fatalf("events = %v", eventTypes)
	}

	loaded, err := manager.Open(result.SessionID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(loaded.Entries))
	}
	if loaded.Entries[0].Kind != session.EntryUser || loaded.Entries[1].Content != "answer" {
		t.Fatalf("entries = %+v", loaded.Entries)
	}
	if loaded.Usage.TotalTokens != 12 {
		t.Fatalf("session usage = %+v", loaded.Usage)
	}
}

func TestClientSessionLifecycle(t *testing.T) {
	c, fake, manager := newTestClient(t)

	first, err := c.Run(context.Background(), "first prompt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	second, err := c.NewSession()
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if second.ID == first.SessionID {
		t.Fatal("NewSession reused the previous session ID")
	}
	if len(fake.History()) != 0 {
		t.Fatalf("agent history after NewSession = %d, want 0", len(fake.History()))
	}

	resumed, err := c.ResumeSession(first.SessionID)
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if resumed.ID != first.SessionID {
		t.Fatalf("resumed = %q, want %q", resumed.ID, first.SessionID)
	}
	if len(fake.History()) != 2 {
		t.Fatalf("resumed history = %d, want 2", len(fake.History()))
	}

	summaries, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("sessions = %+v, want only the first saved session", summaries)
	}

	path, err := c.ExportSession()
	if err != nil {
		t.Fatalf("ExportSession() error = %v", err)
	}
	if !strings.HasSuffix(path, first.SessionID+".md") {
		t.Fatalf("export path = %q", path)
	}

	loaded, err := manager.Open(first.SessionID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	user := loaded.UserEntries()[0]
	forked, err := c.ForkSession(user.ID)
	if err != nil {
		t.Fatalf("ForkSession() error = %v", err)
	}
	if forked.ParentSessionID != first.SessionID {
		t.Fatalf("fork parent = %q", forked.ParentSessionID)
	}
	if len(fake.History()) != 1 {
		t.Fatalf("fork history = %d, want 1", len(fake.History()))
	}
}

func TestClientRejectsConcurrentRun(t *testing.T) {
	c, fake, _ := newTestClient(t)
	fake.block = make(chan struct{})

	firstErr := make(chan error, 1)
	go func() {
		_, err := c.Run(context.Background(), "first")
		firstErr <- err
	}()

	deadline := time.Now().Add(time.Second)
	for !c.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.IsRunning() {
		t.Fatal("client did not enter running state")
	}

	if _, err := c.Run(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Run() error = %v, want ErrBusy", err)
	}

	close(fake.block)
	if err := <-firstErr; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
}

func TestEventRecordIncludesTypeAndEvent(t *testing.T) {
	record := Record(agent.AssistantDelta{At: time.Now(), Text: "hi"})
	if record.Type != agent.EventAssistantDelta {
		t.Fatalf("type = %q", record.Type)
	}
	var delta agent.AssistantDelta
	if err := json.Unmarshal(record.Event, &delta); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if delta.Text != "hi" {
		t.Fatalf("event = %+v", delta)
	}
}

func containsEvent(values []agent.EventType, want agent.EventType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
