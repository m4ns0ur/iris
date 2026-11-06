package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	publicagent "github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

const toolCallRead = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}}]},"finish_reason":"tool_calls"}]}`

const toolCallTwoReads = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}}]},"finish_reason":"tool_calls"}]}`

const toolCallWrite = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"event.txt\",\"content\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`

const finalResponse = `{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`

func writeSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		fmt.Fprintf(w, "data: %s\n\n", payload)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func newTestAgent(t *testing.T, server *httptest.Server, options Options) (*Agent, string) {
	t.Helper()

	pol, err := policy.New(t.TempDir(), policy.Options{
		Mode:            policy.ModeWorkspaceWrite,
		ExternalSandbox: true,
		Shell: policy.ShellOptions{
			Mode:    policy.ShellModeAllow,
			Timeout: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	client := &llm.LLMClient{
		URL:            server.URL,
		MaxRetries:     1,
		RequestTimeout: 5 * time.Second,
		HTTP:           server.Client(),
	}

	instance, err := NewAgent(client, pol, options)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	tempDir := instance.registry.TempDir()
	t.Cleanup(func() {
		_ = instance.Close()
	})

	return instance, tempDir
}

func TestAgentCancelStopsRunAndCleansUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	instance, tempDir := newTestAgent(t, server, Options{})

	first := make(chan struct{})
	var once sync.Once
	instance.Subscribe(func(event publicagent.Event) {
		if delta, ok := event.(publicagent.AssistantDelta); ok && delta.Text == "hello" {
			once.Do(func() { close(first) })
		}
	})
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.Run(context.Background(), "hi")
	}()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first streamed chunk")
	}

	instance.Cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after Cancel")
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp directory %q still exists after Close: %v", tempDir, err)
	}
}

func TestAgentCloseInterruptsActiveRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	instance, tempDir := newTestAgent(t, server, Options{})

	first := make(chan struct{})
	var once sync.Once
	instance.Subscribe(func(event publicagent.Event) {
		if delta, ok := event.(publicagent.AssistantDelta); ok && delta.Text == "hello" {
			once.Do(func() { close(first) })
		}
	})
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.Run(context.Background(), "hi")
	}()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first streamed chunk")
	}

	closeCh := make(chan error, 1)
	go func() {
		closeCh <- instance.Close()
	}()

	select {
	case err := <-closeCh:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not finish after canceling the run")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the canceled run to return")
	}

	if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp directory %q still exists after Close: %v", tempDir, err)
	}
}

func TestAgentRejectsConcurrentAndClosedRuns(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	instance, _ := newTestAgent(t, server, Options{})

	first := make(chan struct{})
	var once sync.Once
	instance.Subscribe(func(event publicagent.Event) {
		if delta, ok := event.(publicagent.AssistantDelta); ok && delta.Text == "hello" {
			once.Do(func() { close(first) })
		}
	})
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.Run(context.Background(), "hi")
	}()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first streamed chunk")
	}

	if err := instance.Run(context.Background(), "again"); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("second Run() error = %v, want ErrAgentBusy", err)
	}

	instance.Cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := instance.Run(context.Background(), "after close"); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("Run() after Close error = %v, want ErrAgentClosed", err)
	}
}

func TestAgentTurnBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeSSE(w, toolCallRead)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{MaxTurns: 1})

	err := instance.Run(context.Background(), "hi")
	if !errors.Is(err, ErrTurnBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrTurnBudgetExceeded", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("LLM requests = %d, want 1", requests.Load())
	}
	if got := countToolMessages(instance.history); got != 1 {
		t.Fatalf("tool messages = %d, want 1", got)
	}
}

func TestAgentToolBudget(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeSSE(w, toolCallTwoReads)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{MaxTurns: 5, MaxToolCalls: 1})

	err := instance.Run(context.Background(), "hi")
	if !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrToolBudgetExceeded", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("LLM requests = %d, want 1", requests.Load())
	}
	if got := countToolMessages(instance.history); got != 2 {
		t.Fatalf("tool messages = %d, want 2 (executed + skipped)", got)
	}
}

func TestAgentToolTimeout(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"sleep 5\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{
		MaxTurns:     5,
		MaxToolCalls: 5,
		ToolTimeout:  100 * time.Millisecond,
	})

	start := time.Now()
	err := instance.Run(context.Background(), "hi")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("tool timeout took too long: %s", elapsed)
	}

	if got := countToolMessages(instance.history); got != 1 {
		t.Fatalf("tool messages = %d, want 1", got)
	}
	if !strings.Contains(historyToolContent(instance.history), "tool execution timed out") {
		t.Fatalf("tool result = %q, want timeout error", historyToolContent(instance.history))
	}
}

func countToolMessages(history []llm.ChatMessage) int {
	count := 0
	for _, message := range history {
		if message.Role == "tool" {
			count++
		}
	}
	return count
}

func historyToolContent(history []llm.ChatMessage) string {
	var builder strings.Builder
	for _, message := range history {
		if message.Role == "tool" {
			fmt.Fprint(&builder, message.Content)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func TestAgentEmitsTypedEvents(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, toolCallWrite)
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"reasoning_content":"thinking","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	var mu sync.Mutex
	seen := make(map[publicagent.EventType]int)
	instance.Subscribe(func(event publicagent.Event) {
		mu.Lock()
		seen[event.Type()]++
		mu.Unlock()
	})

	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, eventType := range []publicagent.EventType{
		publicagent.EventAgentStarted,
		publicagent.EventTurnStarted,
		publicagent.EventReasoningDelta,
		publicagent.EventAssistantDelta,
		publicagent.EventToolProgress,
		publicagent.EventToolStarted,
		publicagent.EventToolCompleted,
		publicagent.EventUsage,
		publicagent.EventAgentFinished,
	} {
		if seen[eventType] == 0 {
			t.Fatalf("event %q was not emitted; seen = %v", eventType, seen)
		}
	}
}

func TestAgentAllowsRerunAfterFinishedEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	finished := make(chan struct{}, 1)
	instance.Subscribe(func(event publicagent.Event) {
		if _, ok := event.(publicagent.AgentFinished); ok {
			select {
			case finished <- struct{}{}:
			default:
			}
		}
	})

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- instance.Run(context.Background(), "one")
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent.finished")
	}

	// The runtime must have cleared its active-run state before emitting the
	// terminal event, so a consumer can immediately start the next run.
	if err := instance.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run() error = %v, want nil", err)
	}

	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Run() did not return")
	}
}
