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
