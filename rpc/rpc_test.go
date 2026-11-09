package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/client"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/session"
)

type fakeAgent struct {
	mu       sync.Mutex
	history  []provider.Message
	handlers []func(agent.Event)
	block    chan struct{}
	canceled chan struct{}
	once     sync.Once
	approver agent.Approver
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
		case <-f.canceled:
			f.emit(agent.AgentFinished{At: time.Now(), Canceled: true})
			return context.Canceled
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

func (f *fakeAgent) Cancel() {
	if f.canceled == nil {
		return
	}
	f.once.Do(func() { close(f.canceled) })
}

func (f *fakeAgent) Close() error                        { return nil }
func (f *fakeAgent) SetApprover(approver agent.Approver) { f.approver = approver }
func (f *fakeAgent) Approver() agent.Approver            { return f.approver }
func (f *fakeAgent) ApprovalTimeout() time.Duration      { return time.Minute }

func newTestServer(t *testing.T) (*Server, *fakeAgent, *session.JSONLManager) {
	t.Helper()

	manager, err := session.NewJSONL(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONL() error = %v", err)
	}
	fake := &fakeAgent{}
	c, err := client.New(client.Options{
		Agent:           fake,
		Sessions:        manager,
		SessionDefaults: session.CreateOptions{Model: "test-model"},
	})
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	return NewServer(c), fake, manager
}

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func decodeFrames(t *testing.T, output string) []frame {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	frames := make([]frame, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var value frame
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("decode frame %q: %v", line, err)
		}
		frames = append(frames, value)
	}
	return frames
}

func findResponse(t *testing.T, frames []frame, id string) frame {
	t.Helper()
	for _, value := range frames {
		if string(value.ID) == id {
			return value
		}
	}
	t.Fatalf("response %s not found in %+v", id, frames)
	return frame{}
}

func TestServerRunsAgentAndStreamsEvents(t *testing.T) {
	server, _, manager := newTestServer(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"agent.run","params":{"prompt":"hello"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"agent.status"}`,
	}, "\n")

	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	frames := decodeFrames(t, output.String())

	ping := findResponse(t, frames, "1")
	if string(ping.Result) != `"pong"` {
		t.Fatalf("ping result = %s", ping.Result)
	}

	run := findResponse(t, frames, "2")
	if run.Error != nil {
		t.Fatalf("agent.run error = %+v", run.Error)
	}
	var result client.Result
	if err := json.Unmarshal(run.Result, &result); err != nil {
		t.Fatalf("decode run result: %v", err)
	}
	if result.Content != "answer" || result.SessionID == "" || result.Usage.TotalTokens != 12 {
		t.Fatalf("run result = %+v", result)
	}

	eventTypes := make(map[string]bool)
	for _, value := range frames {
		if value.Method != "agent.event" {
			continue
		}
		var record struct {
			Type  string          `json:"type"`
			Event json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(value.Params, &record); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		eventTypes[record.Type] = true
	}
	for _, want := range []string{"agent.started", "assistant.delta", "usage", "agent.finished"} {
		if !eventTypes[want] {
			t.Fatalf("missing event %q; got %v", want, eventTypes)
		}
	}

	summaries, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("persisted sessions = %d, want 1", len(summaries))
	}
}

func TestServerSessionMethods(t *testing.T) {
	server, _, _ := newTestServer(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"session.current"}`,
		`{"jsonrpc":"2.0","id":2,"method":"session.new"}`,
		`{"jsonrpc":"2.0","id":3,"method":"session.current"}`,
		`{"jsonrpc":"2.0","id":4,"method":"session.list"}`,
	}, "\n")

	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	frames := decodeFrames(t, output.String())

	first := findResponse(t, frames, "1")
	var firstCurrent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.Result, &firstCurrent); err != nil || firstCurrent.ID == "" {
		t.Fatalf("first current = %s, err=%v", first.Result, err)
	}
	created := findResponse(t, frames, "2")
	var info sessionInfo
	if err := json.Unmarshal(created.Result, &info); err != nil || info.ID == "" {
		t.Fatalf("session.new result = %s, err=%v", created.Result, err)
	}
	if info.ID == firstCurrent.ID {
		t.Fatal("session.new reused the current ID")
	}
	current := findResponse(t, frames, "3")
	if err := json.Unmarshal(current.Result, &firstCurrent); err != nil || firstCurrent.ID != info.ID {
		t.Fatalf("current after new = %s, err=%v", current.Result, err)
	}
	list := findResponse(t, frames, "4")
	if string(list.Result) != "[]" {
		t.Fatalf("session.list result = %s, want []", list.Result)
	}
}

func TestServerBusyCancelAndShutdown(t *testing.T) {
	server, fake, _ := newTestServer(t)
	fake.block = make(chan struct{})
	fake.canceled = make(chan struct{})

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"agent.run","params":{"prompt":"slow"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session.new"}`,
		`{"jsonrpc":"2.0","id":3,"method":"agent.cancel"}`,
		`{"jsonrpc":"2.0","id":4,"method":"shutdown"}`,
	}, "\n")

	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	frames := decodeFrames(t, output.String())

	busy := findResponse(t, frames, "2")
	if busy.Error == nil || busy.Error.Code != codeBusy {
		t.Fatalf("session.new during run = %+v, want busy", busy.Error)
	}
	cancel := findResponse(t, frames, "3")
	if !strings.Contains(string(cancel.Result), "true") {
		t.Fatalf("agent.cancel result = %s", cancel.Result)
	}
	shutdown := findResponse(t, frames, "4")
	if !strings.Contains(string(shutdown.Result), "true") {
		t.Fatalf("shutdown result = %s", shutdown.Result)
	}
}

func TestServerInvalidRequest(t *testing.T) {
	server, _, _ := newTestServer(t)
	input := strings.Join([]string{
		`not json`,
	}, "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	frames := decodeFrames(t, output.String())
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != codeParse {
		t.Fatalf("frames = %+v", frames)
	}
}
