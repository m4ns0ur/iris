package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/agent"
	internalagent "github.com/m4ns0ur/iris/internal/agent"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
	apitype "github.com/m4ns0ur/iris/tool"
)

type fakeAgent struct {
	mu        sync.Mutex
	history   []provider.Message
	handlers  []func(agent.Event)
	approver  agent.Approver
	block     chan struct{}
	runEvents []agent.Event
	steering  []string
	followUps []string
}

func (f *fakeAgent) Run(ctx context.Context, input string) error {
	f.mu.Lock()
	f.history = append(f.history, provider.Message{
		Role:    "user",
		Content: []provider.Content{{Type: "text", Text: input}},
	})
	f.mu.Unlock()

	f.emit(agent.AgentStarted{At: time.Now(), Input: input})
	if f.runEvents != nil {
		for _, event := range f.runEvents {
			f.emit(event)
		}
		f.emit(agent.AgentFinished{At: time.Now()})
		return nil
	}
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

func (f *fakeAgent) Cancel() {}
func (f *fakeAgent) Abort() agent.Queue {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := agent.Queue{
		Steering:  append([]string(nil), f.steering...),
		FollowUps: append([]string(nil), f.followUps...),
	}
	f.steering = nil
	f.followUps = nil
	return queue
}
func (f *fakeAgent) Steer(input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steering = append(f.steering, input)
	return nil
}
func (f *fakeAgent) FollowUp(input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.followUps = append(f.followUps, input)
	return nil
}
func (f *fakeAgent) ClearQueue() agent.Queue {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := agent.Queue{
		Steering:  append([]string(nil), f.steering...),
		FollowUps: append([]string(nil), f.followUps...),
	}
	f.steering = nil
	f.followUps = nil
	return queue
}
func (f *fakeAgent) Pending() agent.Queue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return agent.Queue{
		Steering:  append([]string(nil), f.steering...),
		FollowUps: append([]string(nil), f.followUps...),
	}
}
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

func TestClientExecutionControlDelegatesToAgent(t *testing.T) {
	c, fake, _ := newTestClient(t)

	if err := c.Steer("change direction"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if err := c.FollowUp("then summarize"); err != nil {
		t.Fatalf("FollowUp() error = %v", err)
	}

	pending := c.Pending()
	if len(pending.Steering) != 1 || pending.Steering[0] != "change direction" {
		t.Fatalf("pending steering = %+v", pending.Steering)
	}
	if len(pending.FollowUps) != 1 || pending.FollowUps[0] != "then summarize" {
		t.Fatalf("pending follow-ups = %+v", pending.FollowUps)
	}

	cleared := c.ClearQueue()
	if len(cleared.Steering) != 1 || len(cleared.FollowUps) != 1 {
		t.Fatalf("ClearQueue() = %+v", cleared)
	}
	if pending := fake.Pending(); len(pending.Steering) != 0 || len(pending.FollowUps) != 0 {
		t.Fatalf("agent queue after clear = %+v", pending)
	}
}

func TestClientResetsPartialOutputOnTurnRetry(t *testing.T) {
	c, fake, _ := newTestClient(t)
	fake.runEvents = []agent.Event{
		agent.TurnStarted{At: time.Now(), Turn: 0},
		agent.AssistantDelta{At: time.Now(), Text: "partial"},
		agent.TurnRetried{At: time.Now(), Turn: 0, Reason: "context_overflow", NextAttempt: 2},
		agent.AssistantDelta{At: time.Now(), Text: "final"},
		agent.TurnFinished{At: time.Now(), Turn: 0, Attempts: 2, FinishReason: "stop", ContentBytes: 5},
	}

	result, err := c.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "final" {
		t.Fatalf("content = %q, want only the retried attempt", result.Content)
	}
}

func TestClientReportsTruncatedTurn(t *testing.T) {
	c, fake, _ := newTestClient(t)
	fake.runEvents = []agent.Event{
		agent.TurnStarted{At: time.Now(), Turn: 0},
		agent.AssistantDelta{At: time.Now(), Text: "cut"},
		agent.TurnFinished{At: time.Now(), Turn: 0, Attempts: 1, FinishReason: "length", ContentBytes: 3},
	}

	result, err := c.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result = %+v, want truncated", result)
	}
}

func TestClientEmptyFinalTurnDoesNotReuseEarlierAssistantText(t *testing.T) {
	c, fake, _ := newTestClient(t)
	fake.history = []provider.Message{
		{Role: "user", Content: []provider.Content{{Type: "text", Text: "earlier"}}},
		{Role: "assistant", Content: "stale answer"},
	}
	fake.runEvents = []agent.Event{
		agent.TurnStarted{At: time.Now(), Turn: 0},
		agent.TurnFinished{At: time.Now(), Turn: 0, Attempts: 1, FinishReason: "stop"},
	}

	result, err := c.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "" {
		t.Fatalf("content = %q, want empty (stale assistant text must not be reused)", result.Content)
	}
}

func TestClientFallsBackToHistoryForNonStreamingResult(t *testing.T) {
	c, fake, _ := newTestClient(t)
	fake.history = []provider.Message{
		{Role: "user", Content: []provider.Content{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: "answer from history"},
	}
	fake.runEvents = []agent.Event{
		agent.TurnStarted{At: time.Now(), Turn: 0},
		agent.TurnFinished{At: time.Now(), Turn: 0, Attempts: 1, FinishReason: "stop", ContentBytes: len("answer from history")},
	}

	result, err := c.Run(context.Background(), "next")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "answer from history" {
		t.Fatalf("content = %q", result.Content)
	}
}

type inspectSessionTool struct {
	manager  *session.JSONLManager
	id       func() string
	observed chan *session.Session
}

func (t *inspectSessionTool) Name() string            { return "inspect" }
func (t *inspectSessionTool) Description() string     { return "Inspect the session" }
func (t *inspectSessionTool) FullDescription() string { return "Read the current session journal." }
func (t *inspectSessionTool) Parameters() provider.Parameters {
	return provider.Parameters{Type: "object"}
}
func (t *inspectSessionTool) Execute(context.Context, map[string]any) (map[string]any, error) {
	current, err := t.manager.Open(t.id())
	if err != nil {
		return nil, err
	}
	t.observed <- current
	return map[string]any{"ok": true}, nil
}

func TestClientRecordsDurablyBeforeToolExecution(t *testing.T) {
	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	pol, err := policy.New(t.TempDir(), policy.Options{
		Mode:            policy.ModeWorkspaceWrite,
		ExternalSandbox: true,
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	var currentID string
	inspect := &inspectSessionTool{
		manager:  manager,
		id:       func() string { return currentID },
		observed: make(chan *session.Session, 1),
	}
	beforeFinal := make(chan *session.Session, 1)
	var calls atomic.Int32
	prov := provider.ResponseFunc(func(context.Context, provider.Request) (provider.Response, error) {
		if calls.Add(1) == 1 {
			return provider.Response{
				FinishReason: "tool_calls",
				ToolCalls: []provider.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: provider.FunctionCall{
						Name:      "inspect",
						Arguments: `{}`,
					},
				}},
			}, nil
		}
		current, err := manager.Open(currentID)
		if err != nil {
			return provider.Response{}, err
		}
		beforeFinal <- current
		return provider.Response{Content: "done", FinishReason: "stop"}, nil
	})

	runtimeAgent, err := internalagent.NewAgent(prov, pol, internalagent.Options{
		Tools: []apitype.Tool{inspect},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer runtimeAgent.Close()

	c, err := New(Options{
		Agent:    runtimeAgent,
		Sessions: manager,
		SessionDefaults: session.CreateOptions{
			Model:     "test-model",
			Workspace: "/workspace",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	currentID = c.SessionID()

	result, err := c.Run(context.Background(), "inspect the session")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "done" {
		t.Fatalf("result content = %q", result.Content)
	}

	var observed *session.Session
	select {
	case observed = <-inspect.observed:
	case <-time.After(time.Second):
		t.Fatal("tool did not observe the session before execution")
	}
	if len(observed.Entries) != 2 {
		t.Fatalf("entries during tool execution = %d, want user + assistant tool call: %+v", len(observed.Entries), observed.Entries)
	}
	if observed.Entries[0].Kind != session.EntryUser || observed.Entries[0].Content != "inspect the session" {
		t.Fatalf("durable user entry = %+v", observed.Entries[0])
	}
	if observed.Entries[1].Kind != session.EntryAssistant ||
		len(observed.Entries[1].ToolCalls) != 1 ||
		observed.Entries[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("durable assistant entry = %+v", observed.Entries[1])
	}

	var beforeFinalSession *session.Session
	select {
	case beforeFinalSession = <-beforeFinal:
	case <-time.After(time.Second):
		t.Fatal("provider did not observe the session before the final turn")
	}
	if len(beforeFinalSession.Entries) != 3 {
		t.Fatalf("entries before final turn = %d, want user + assistant + tool result: %+v", len(beforeFinalSession.Entries), beforeFinalSession.Entries)
	}
	if beforeFinalSession.Entries[2].Kind != session.EntryTool || beforeFinalSession.Entries[2].Content != `{"ok":true}` {
		t.Fatalf("tool result was not durable before the next turn: %+v", beforeFinalSession.Entries[2])
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	final, err := manager.Open(currentID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(final.Entries) != 4 {
		t.Fatalf("final entries = %d, want 4: %+v", len(final.Entries), final.Entries)
	}
	if final.Entries[2].Kind != session.EntryTool || final.Entries[2].Content != `{"ok":true}` {
		t.Fatalf("durable tool result = %+v", final.Entries[2])
	}
	if final.Entries[3].Kind != session.EntryAssistant || final.Entries[3].Content != "done" {
		t.Fatalf("durable final assistant = %+v", final.Entries[3])
	}
	if final.Revision < 2 {
		t.Fatalf("revision = %d, want at least 2", final.Revision)
	}

	lease, err := manager.Acquire(final)
	if err != nil {
		t.Fatalf("Acquire() after Close error = %v, want released lease", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestCompactionDoesNotRewriteSessionTranscript(t *testing.T) {
	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	base, err := manager.Create(session.CreateOptions{Model: "test-model", Workspace: "/workspace"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	lease, err := manager.Acquire(base)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	long := strings.Repeat("durable transcript detail ", 200)
	for index := 0; index < 4; index++ {
		if _, err := manager.Append(base, session.Entry{
			Kind:    session.EntryUser,
			Content: fmt.Sprintf("request %d %s", index, long),
		}); err != nil {
			t.Fatalf("Append(user) error = %v", err)
		}
		if _, err := manager.Append(base, session.Entry{
			Kind:    session.EntryAssistant,
			Content: fmt.Sprintf("answer %d %s", index, long),
		}); err != nil {
			t.Fatalf("Append(assistant) error = %v", err)
		}
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	pol, err := policy.New(t.TempDir(), policy.Options{
		Mode:            policy.ModeWorkspaceWrite,
		ExternalSandbox: true,
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}
	prov := provider.ResponseFunc(func(_ context.Context, request provider.Request) (provider.Response, error) {
		if len(request.Tools) == 0 {
			return provider.Response{
				Content:      "compacted summary",
				FinishReason: "stop",
				Usage:        provider.Usage{PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35},
			}, nil
		}
		return provider.Response{Content: "final answer", FinishReason: "stop"}, nil
	})
	runtimeAgent, err := internalagent.NewAgent(prov, pol, internalagent.Options{
		ContextWindow:    2048,
		ReserveTokens:    512,
		CompactAt:        0.5,
		KeepRecentTokens: 256,
		SummaryMaxTokens: 128,
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer runtimeAgent.Close()

	c, err := New(Options{
		Agent:          runtimeAgent,
		Sessions:       manager,
		InitialSession: base,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := c.Run(context.Background(), "continue working")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Content != "final answer" {
		t.Fatalf("result content = %q", result.Content)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	final, err := manager.Open(base.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if len(final.Entries) != 10 {
		t.Fatalf("transcript entries = %d, want 8 original + user + assistant", len(final.Entries))
	}
	for _, entry := range final.Entries {
		if entry.Content == "compacted summary" {
			t.Fatalf("compaction summary replaced the transcript: %+v", final.Entries)
		}
	}
	if !strings.HasPrefix(final.Entries[0].Content, "request 0 ") {
		t.Fatalf("original transcript lost: %+v", final.Entries[0])
	}
}
