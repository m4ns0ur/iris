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

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/internal/policy"
)

const toolCallRead = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}}]},"finish_reason":"tool_calls"}]}`

const toolCallTwoReads = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{\"path\":\"missing.txt\"}"}}]},"finish_reason":"tool_calls"}]}`

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
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.RunContext(context.Background(), "hi", func(_ string, content string) {
			if content == "hello" {
				once.Do(func() { close(first) })
			}
		}, nil)
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
			t.Fatalf("RunContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContext() did not return after Cancel")
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
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.RunContext(context.Background(), "hi", func(_ string, content string) {
			if content == "hello" {
				once.Do(func() { close(first) })
			}
		}, nil)
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
			t.Fatalf("RunContext() error = %v, want context.Canceled", err)
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
	errCh := make(chan error, 1)

	go func() {
		errCh <- instance.RunContext(context.Background(), "hi", func(_ string, content string) {
			if content == "hello" {
				once.Do(func() { close(first) })
			}
		}, nil)
	}()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first streamed chunk")
	}

	if err := instance.RunContext(context.Background(), "again", nil, nil); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("second RunContext() error = %v, want ErrAgentBusy", err)
	}

	instance.Cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunContext() error = %v, want context.Canceled", err)
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := instance.RunContext(context.Background(), "after close", nil, nil); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("RunContext() after Close error = %v, want ErrAgentClosed", err)
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

	err := instance.RunContext(context.Background(), "hi", nil, nil)
	if !errors.Is(err, ErrTurnBudgetExceeded) {
		t.Fatalf("RunContext() error = %v, want ErrTurnBudgetExceeded", err)
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

	err := instance.RunContext(context.Background(), "hi", nil, nil)
	if !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("RunContext() error = %v, want ErrToolBudgetExceeded", err)
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
	err := instance.RunContext(context.Background(), "hi", nil, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
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
