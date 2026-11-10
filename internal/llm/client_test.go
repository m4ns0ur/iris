package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

func newTestClient(server *httptest.Server) *LLMClient {
	return &LLMClient{
		URL:            server.URL,
		MaxRetries:     1,
		RequestTimeout: 5 * time.Second,
		HTTP:           server.Client(),
	}
}

func TestStreamCancellation(t *testing.T) {
	firstChunk := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not support flushing")
			return
		}
		flusher.Flush()

		<-r.Context().Done()
	}))
	defer server.Close()

	client := newTestClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := client.Stream(ctx, provider.Request{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		for event := range events {
			switch value := event.(type) {
			case provider.DeltaEvent:
				if value.Delta.Content == "first" {
					once.Do(func() { close(firstChunk) })
				}
			case provider.ErrorEvent:
				errCh <- value.Err
				return
			case provider.DoneEvent:
				errCh <- nil
				return
			}
		}
		if ctx.Err() != nil {
			errCh <- ctx.Err()
			return
		}
		errCh <- errors.New("stream closed without a terminal event")
	}()

	select {
	case <-firstChunk:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first streamed chunk")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestStreamRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()

		<-r.Context().Done()
	}))
	defer server.Close()

	client := &LLMClient{
		URL:            server.URL,
		MaxRetries:     1,
		RequestTimeout: 100 * time.Millisecond,
		HTTP:           server.Client(),
	}

	_, err := provider.Collect(context.Background(), client, provider.Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Collect() error = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout explanation", err)
	}
}

func TestStreamRetryCancellation(t *testing.T) {
	var requests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &LLMClient{
		URL:            server.URL,
		MaxRetries:     3,
		RequestTimeout: 10 * time.Second,
		HTTP:           server.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Stream(ctx, provider.Request{})
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("retry cancellation took too long: %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream() did not return after cancelling retries")
	}

	if requests.Load() < 1 {
		t.Fatal("expected at least one request before cancellation")
	}
}

func TestStreamRetriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "try again", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := &LLMClient{
		URL:            server.URL,
		MaxRetries:     2,
		RetryBaseDelay: 10 * time.Millisecond,
		RequestTimeout: 5 * time.Second,
		HTTP:           server.Client(),
	}

	response, err := provider.Collect(context.Background(), client, provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got := response.Content; got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
	if response.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want 12 total tokens", response.Usage)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestStreamDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	client := &LLMClient{
		URL:            server.URL,
		MaxRetries:     3,
		RequestTimeout: 5 * time.Second,
		HTTP:           server.Client(),
	}

	_, err := client.Stream(context.Background(), provider.Request{})
	if err == nil {
		t.Fatal("Stream() error = nil, want HTTP status error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (4xx must not be retried)", attempts.Load())
	}
}

func TestStreamPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &LLMClient{
		URL:            "http://127.0.0.1:1/chat/completions",
		MaxRetries:     1,
		RequestTimeout: time.Second,
	}

	_, err := client.Stream(ctx, provider.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v, want context.Canceled", err)
	}
}

func TestStreamWrapsContextWindowClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "maximum context length exceeded", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newTestClient(server)
	_, err := client.Stream(context.Background(), provider.Request{})
	if !errors.Is(err, provider.ErrContextWindow) {
		t.Fatalf("Stream() error = %v, want provider.ErrContextWindow", err)
	}
	if !provider.IsContextWindowError(err) {
		t.Fatalf("IsContextWindowError(%v) = false", err)
	}
}

func TestStreamAccumulatesSplitToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":".txt\",\"content\":\"x\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	response, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "write" {
		t.Fatalf("tool call = %+v", call)
	}
	if call.Function.Arguments != `{"path":"a.txt","content":"x"}` {
		t.Fatalf("arguments = %q", call.Function.Arguments)
	}
	if response.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", response.FinishReason)
	}
}

func TestStreamSeparatesInterleavedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"read","arguments":"{\"path\":\"a"}},{"index":1,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"b"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"}"}},{"index":0,"function":{"arguments":"\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	response, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(response.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(response.ToolCalls))
	}
	if response.ToolCalls[0].ID != "call_0" || response.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("call 0 = %+v", response.ToolCalls[0])
	}
	if response.ToolCalls[1].ID != "call_1" || response.ToolCalls[1].Function.Arguments != `{"path":"b"}` {
		t.Fatalf("call 1 = %+v", response.ToolCalls[1])
	}
}

func TestStreamSeparatesIDOnlyToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}},{"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	response, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(response.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(response.ToolCalls))
	}
	if response.ToolCalls[0].ID != "call_a" || response.ToolCalls[1].ID != "call_b" {
		t.Fatalf("tool calls = %+v", response.ToolCalls)
	}
}

func TestStreamAccumulatesRepeatedToolNameDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"a"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"name":"read","arguments":"\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	response, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	if got := response.ToolCalls[0].Function.Arguments; got != `{"path":"a"}` {
		t.Fatalf("arguments = %q", got)
	}
}

func TestStreamInterruptedBeforeCompletion(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// Close without finish_reason or [DONE].
	}))
	defer server.Close()

	client := newTestClient(server)
	client.MaxRetries = 3
	_, err := provider.Collect(context.Background(), client, provider.Request{})
	if !errors.Is(err, provider.ErrStreamInterrupted) {
		t.Fatalf("Collect() error = %v, want ErrStreamInterrupted", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry after the stream starts)", attempts.Load())
	}
}

func TestStreamAcceptsFinishReasonWithoutDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		// Some compatible servers close without [DONE].
	}))
	defer server.Close()

	response, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.Content != "ok" || response.FinishReason != "stop" {
		t.Fatalf("response = %+v", response)
	}
}

func TestStreamRejectsMalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not json}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	_, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if !errors.Is(err, provider.ErrInvalidStream) {
		t.Fatalf("Collect() error = %v, want ErrInvalidStream", err)
	}
}

func TestStreamReturnsEmbeddedErrorChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"error":{"message":"model crashed"}}`+"\n\n")
	}))
	defer server.Close()

	_, err := provider.Collect(context.Background(), newTestClient(server), provider.Request{})
	if err == nil || !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("Collect() error = %v, want embedded error", err)
	}
}

func TestStreamRetriesTooManyRequests(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(server)
	client.MaxRetries = 2
	client.RetryBaseDelay = 10 * time.Millisecond

	response, err := provider.Collect(context.Background(), client, provider.Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.Content != "ok" || attempts.Load() != 2 {
		t.Fatalf("response = %+v, attempts = %d", response, attempts.Load())
	}
}

func TestRetryableStatusClassification(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusBadRequest:          false,
		http.StatusUnauthorized:        false,
		http.StatusRequestTimeout:      true,
		http.StatusTooEarly:            true,
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusBadGateway:          true,
		http.StatusServiceUnavailable:  true,
	} {
		if got := retryableStatus(status); got != want {
			t.Fatalf("retryableStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	if got := parseRetryAfter("3", now); got != 3*time.Second {
		t.Fatalf("seconds = %s, want 3s", got)
	}
	if got := parseRetryAfter(now.Add(5*time.Second).UTC().Format(http.TimeFormat), now); got < 4*time.Second || got > 5*time.Second {
		t.Fatalf("date = %s, want about 5s", got)
	}
	if got := parseRetryAfter("not-a-date", now); got != 0 {
		t.Fatalf("invalid = %s, want 0", got)
	}
	if got := parseRetryAfter("-1", now); got != 0 {
		t.Fatalf("negative = %s, want 0", got)
	}
}

func TestStreamWrapsContextWindowServerErrorWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "maximum context length exceeded", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(server)
	client.MaxRetries = 3
	client.RetryBaseDelay = 10 * time.Millisecond

	_, err := client.Stream(context.Background(), provider.Request{})
	if !errors.Is(err, provider.ErrContextWindow) {
		t.Fatalf("Stream() error = %v, want ErrContextWindow", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (context errors must not be retried)", attempts.Load())
	}
}
