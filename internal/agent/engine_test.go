package agent

import (
	"context"
	"encoding/json"
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
	"github.com/m4ns0ur/iris/provider"
	apitype "github.com/m4ns0ur/iris/tool"
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
		publicagent.EventTurnFinished,
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

func TestAgentLoadHistoryAndSnapshot(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	fake := provider.ResponseFunc(func(
		context.Context,
		provider.Request,
	) (provider.Response, error) {
		return provider.Response{Content: "ok", FinishReason: "stop"}, nil
	})

	instance, err := NewAgent(fake, pol)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	if got := instance.History(); len(got) != 0 {
		t.Fatalf("initial history = %d, want 0", len(got))
	}

	messages := []provider.Message{
		{Role: "user", Content: []provider.Content{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: "hello"},
	}
	if err := instance.LoadHistory(messages); err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	history := instance.History()
	if len(history) != 3 {
		t.Fatalf("history = %d, want 3 (system + 2)", len(history))
	}
	if history[0].Role != "system" || history[1].Role != "user" || history[2].Role != "assistant" {
		t.Fatalf("history roles = %q, %q, %q", history[0].Role, history[1].Role, history[2].Role)
	}

	// History is a snapshot, not the internal slice.
	history[1].Role = "mutated"
	if got := instance.History()[1].Role; got != "user" {
		t.Fatalf("history was mutated through snapshot: %q", got)
	}

	if err := instance.LoadHistory(nil); err != nil {
		t.Fatalf("LoadHistory(nil) error = %v", err)
	}
	if got := instance.History(); len(got) != 0 {
		t.Fatalf("cleared history = %d, want 0", len(got))
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := instance.LoadHistory(messages); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("LoadHistory() after Close error = %v, want ErrAgentClosed", err)
	}
}

const toolCallWait = `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_wait","type":"function","function":{"name":"wait","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`

// waitTool blocks until the test releases it, giving the test a deterministic
// window in which a run is active and executing a tool.
type waitTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (t *waitTool) Name() string                    { return "wait" }
func (t *waitTool) Description() string             { return "Wait for the test" }
func (t *waitTool) FullDescription() string         { return "Wait until the test releases the tool." }
func (t *waitTool) Parameters() provider.Parameters { return provider.Parameters{Type: "object"} }
func (t *waitTool) Execute(ctx context.Context, _ map[string]any) (map[string]any, error) {
	t.once.Do(func() { close(t.started) })
	select {
	case <-t.release:
		return map[string]any{"status": "ok"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAgentSteeringDeliveredAfterToolExecution(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, toolCallWait)
			return
		}
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	wait := &waitTool{started: make(chan struct{}), release: make(chan struct{})}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{wait}})

	var mu sync.Mutex
	var delivered []publicagent.InstructionDelivered
	var queueUpdates []publicagent.QueueUpdated
	instance.Subscribe(func(event publicagent.Event) {
		switch e := event.(type) {
		case publicagent.InstructionDelivered:
			mu.Lock()
			delivered = append(delivered, e)
			mu.Unlock()
		case publicagent.QueueUpdated:
			mu.Lock()
			queueUpdates = append(queueUpdates, e)
			mu.Unlock()
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- instance.Run(context.Background(), "implement the feature")
	}()

	select {
	case <-wait.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the tool to start")
	}

	const instruction = "Stop. Do not modify the database schema; use the existing structure."
	if err := instance.Steer(instruction); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if queue := instance.Pending(); len(queue.Steering) != 1 || queue.Steering[0] != instruction {
		t.Fatalf("pending queue = %+v, want the steering instruction", queue)
	}

	close(wait.release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not finish")
	}

	if requests.Load() != 2 {
		t.Fatalf("LLM requests = %d, want 2 (tool turn + steered turn)", requests.Load())
	}

	history := instance.History()
	steerIndex := messageIndex(history, "user", instruction)
	if steerIndex < 0 {
		t.Fatalf("steering instruction missing from history: %+v", history)
	}
	toolIndex := lastRoleIndex(history, "tool")
	if toolIndex < 0 || steerIndex < toolIndex {
		t.Fatalf("steering message index = %d, last tool index = %d; want after tool results", steerIndex, toolIndex)
	}
	if history[len(history)-1].Role != "assistant" {
		t.Fatalf("last history message role = %q, want assistant", history[len(history)-1].Role)
	}

	if queue := instance.Pending(); len(queue.Steering) != 0 || len(queue.FollowUps) != 0 {
		t.Fatalf("pending queue after run = %+v, want empty", queue)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0].Kind != publicagent.InstructionSteering || delivered[0].Text != instruction {
		t.Fatalf("delivered instructions = %+v", delivered)
	}
	if len(queueUpdates) == 0 {
		t.Fatal("no queue.updated event was emitted")
	}
	last := queueUpdates[len(queueUpdates)-1]
	if len(last.Steering) != 0 || len(last.FollowUps) != 0 {
		t.Fatalf("last queue.updated = %+v, want empty", last)
	}
}

func TestAgentSteeringRevivesRunAtStopPoint(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, finalResponse)
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"revised"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	const instruction = "Use the existing structure instead."
	var once sync.Once
	var steerErr error
	instance.Subscribe(func(event publicagent.Event) {
		delta, ok := event.(publicagent.AssistantDelta)
		if !ok || delta.Text != "done" {
			return
		}
		once.Do(func() {
			steerErr = instance.Steer(instruction)
		})
	})

	if err := instance.Run(context.Background(), "implement the feature"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if steerErr != nil {
		t.Fatalf("Steer() from event handler error = %v", steerErr)
	}
	if requests.Load() != 2 {
		t.Fatalf("LLM requests = %d, want 2 (steering revived the run)", requests.Load())
	}

	history := instance.History()
	if messageIndex(history, "user", instruction) < 0 {
		t.Fatalf("steering instruction missing from history: %+v", history)
	}
	if got := history[len(history)-1]; got.Role != "assistant" || messageText(got) != "revised" {
		t.Fatalf("last message = %+v, want the revised assistant response", got)
	}
}

func TestAgentFollowUpDeliveredOnlyAtStop(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, finalResponse)
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"followed up"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	const instruction = "After you finish, summarize the change."
	var once sync.Once
	var followErr error
	var delivered []publicagent.InstructionDelivered
	instance.Subscribe(func(event publicagent.Event) {
		switch e := event.(type) {
		case publicagent.AssistantDelta:
			if e.Text == "done" {
				once.Do(func() { followErr = instance.FollowUp(instruction) })
			}
		case publicagent.InstructionDelivered:
			delivered = append(delivered, e)
		}
	})

	if err := instance.Run(context.Background(), "implement the feature"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if followErr != nil {
		t.Fatalf("FollowUp() from event handler error = %v", followErr)
	}
	if requests.Load() != 2 {
		t.Fatalf("LLM requests = %d, want 2 (follow-up continued the run)", requests.Load())
	}
	if len(delivered) != 1 || delivered[0].Kind != publicagent.InstructionFollowUp || delivered[0].Text != instruction {
		t.Fatalf("delivered instructions = %+v", delivered)
	}
	if messageIndex(instance.History(), "user", instruction) < 0 {
		t.Fatalf("follow-up missing from history: %+v", instance.History())
	}
}

func TestAgentInitialInputPrecedesSteering(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, finalResponse)
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	var once sync.Once
	var orderErr error
	instance.Subscribe(func(event publicagent.Event) {
		if delta, ok := event.(publicagent.AssistantDelta); ok && delta.Text == "done" {
			once.Do(func() { orderErr = instance.Steer("follow the new approach") })
		}
	})

	if err := instance.Run(context.Background(), "start the task"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if orderErr != nil {
		t.Fatalf("Steer() error = %v", orderErr)
	}

	history := instance.History()
	first := messageIndex(history, "user", "start the task")
	steer := messageIndex(history, "user", "follow the new approach")
	if first < 0 || steer < 0 || steer < first {
		t.Fatalf("history order incorrect: initial=%d steer=%d history=%+v", first, steer, history)
	}
}

func TestAgentClearQueuePreventsDelivery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, toolCallWait)
			return
		}
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	wait := &waitTool{started: make(chan struct{}), release: make(chan struct{})}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{wait}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- instance.Run(context.Background(), "implement the feature")
	}()

	select {
	case <-wait.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the tool to start")
	}

	if err := instance.Steer("first instruction"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if err := instance.Steer("second instruction"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}

	queue := instance.ClearQueue()
	if len(queue.Steering) != 2 || queue.Steering[0] != "first instruction" || queue.Steering[1] != "second instruction" {
		t.Fatalf("ClearQueue() = %+v", queue)
	}
	if pending := instance.Pending(); len(pending.Steering) != 0 || len(pending.FollowUps) != 0 {
		t.Fatalf("Pending() after clear = %+v, want empty", pending)
	}

	close(wait.release)

	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("LLM requests = %d, want 2 (cleared instructions were not delivered)", requests.Load())
	}
	if messageIndex(instance.History(), "user", "first instruction") >= 0 {
		t.Fatalf("cleared instruction was delivered: %+v", instance.History())
	}
}

func TestAgentInstructionQueueErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	if err := instance.Steer("instruction"); !errors.Is(err, ErrAgentIdle) {
		t.Fatalf("Steer() while idle error = %v, want ErrAgentIdle", err)
	}
	if err := instance.FollowUp("instruction"); !errors.Is(err, ErrAgentIdle) {
		t.Fatalf("FollowUp() while idle error = %v, want ErrAgentIdle", err)
	}
	if err := instance.Steer("   "); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("Steer() blank error = %v, want ErrEmptyInput", err)
	}
	if queue := instance.Pending(); len(queue.Steering) != 0 || len(queue.FollowUps) != 0 {
		t.Fatalf("queue after rejected calls = %+v, want empty", queue)
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := instance.Steer("instruction"); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("Steer() after Close error = %v, want ErrAgentClosed", err)
	}
}

func TestAgentAbortReturnsQueuedInstructionsAndStopsRun(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, toolCallWait)
			return
		}
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	wait := &waitTool{started: make(chan struct{}), release: make(chan struct{})}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{wait}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- instance.Run(context.Background(), "hi")
	}()

	select {
	case <-wait.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the tool to start")
	}

	if err := instance.Steer("do not continue"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if err := instance.FollowUp("then summarize"); err != nil {
		t.Fatalf("FollowUp() error = %v", err)
	}

	queue := instance.Abort()
	if len(queue.Steering) != 1 || queue.Steering[0] != "do not continue" {
		t.Fatalf("Abort() steering = %+v", queue.Steering)
	}
	if len(queue.FollowUps) != 1 || queue.FollowUps[0] != "then summarize" {
		t.Fatalf("Abort() follow-ups = %+v", queue.FollowUps)
	}
	if err := instance.Steer("too late"); !errors.Is(err, ErrAgentIdle) {
		t.Fatalf("Steer() after Abort error = %v, want ErrAgentIdle", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after Abort")
	}

	if got := messageIndex(instance.History(), "user", "do not continue"); got >= 0 {
		t.Fatalf("aborted instruction was delivered: %+v", instance.History())
	}

	// The conversation survives the abort and can be continued without
	// restarting the application.
	if err := instance.Run(context.Background(), "continue with the existing structure"); err != nil {
		t.Fatalf("Run() after Abort error = %v", err)
	}
	history := instance.History()
	if messageIndex(history, "user", "hi") < 0 || messageIndex(history, "user", "continue with the existing structure") < 0 {
		t.Fatalf("history after rerun = %+v", history)
	}
}

func messageIndex(history []provider.Message, role, text string) int {
	for i, message := range history {
		if message.Role == role && messageText(message) == text {
			return i
		}
	}
	return -1
}

func lastRoleIndex(history []provider.Message, role string) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == role {
			return i
		}
	}
	return -1
}

func messageText(message provider.Message) string {
	switch content := message.Content.(type) {
	case string:
		return content
	case []provider.Content:
		var builder strings.Builder
		for _, part := range content {
			builder.WriteString(part.Text)
		}
		return builder.String()
	default:
		return ""
	}
}

func TestAgentSteeringDeliveredBeforeFollowUp(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			writeSSE(w, toolCallWait)
		case 2:
			writeSSE(w, finalResponse)
		default:
			writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"followed"},"finish_reason":"stop"}]}`)
		}
	}))
	defer server.Close()

	wait := &waitTool{started: make(chan struct{}), release: make(chan struct{})}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{wait}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- instance.Run(context.Background(), "implement the feature")
	}()

	select {
	case <-wait.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the tool to start")
	}

	if err := instance.Steer("use the existing structure"); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if err := instance.FollowUp("then summarize the change"); err != nil {
		t.Fatalf("FollowUp() error = %v", err)
	}

	close(wait.release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not finish")
	}

	if requests.Load() != 3 {
		t.Fatalf("LLM requests = %d, want 3 (tool turn, steered turn, follow-up turn)", requests.Load())
	}

	history := instance.History()
	steer := messageIndex(history, "user", "use the existing structure")
	follow := messageIndex(history, "user", "then summarize the change")
	if steer < 0 || follow < 0 {
		t.Fatalf("instructions missing from history: %+v", history)
	}
	if steer >= follow {
		t.Fatalf("steering index = %d, follow-up index = %d; want steering first", steer, follow)
	}
	if got := history[len(history)-1]; got.Role != "assistant" || messageText(got) != "followed" {
		t.Fatalf("last message = %+v, want the follow-up response", got)
	}
}

type toolCallSpec struct {
	ID   string
	Name string
	Args string
}

// sseToolCalls builds one SSE payload containing tool-call deltas.
func sseToolCalls(finish string, specs ...toolCallSpec) string {
	calls := make([]any, 0, len(specs))
	for index, spec := range specs {
		calls = append(calls, map[string]any{
			"index": index,
			"id":    spec.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      spec.Name,
				"arguments": spec.Args,
			},
		})
	}

	payload := map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"tool_calls": calls},
			"finish_reason": finish,
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

type recordingTool struct {
	count     atomic.Int32
	err       error
	required  []string
	onExecute func()
}

func (t *recordingTool) Name() string            { return "record" }
func (t *recordingTool) Description() string     { return "Record a call" }
func (t *recordingTool) FullDescription() string { return "Record that the tool was invoked." }
func (t *recordingTool) Parameters() provider.Parameters {
	return provider.Parameters{
		Type:     "object",
		Required: append([]string(nil), t.required...),
		Properties: map[string]provider.Property{
			"path": {Type: "string"},
		},
	}
}
func (t *recordingTool) Execute(context.Context, map[string]any) (map[string]any, error) {
	t.count.Add(1)
	if t.onExecute != nil {
		t.onExecute()
	}
	if t.err != nil {
		return nil, t.err
	}
	return map[string]any{"ok": true}, nil
}

type testToolErrorPayload struct {
	OK    bool `json:"ok"`
	Error struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		Tool       string `json:"tool"`
		ToolCallID string `json:"tool_call_id"`
		Executed   bool   `json:"executed"`
		Retryable  bool   `json:"retryable"`
	} `json:"error"`
}

func parseToolErrorResult(t *testing.T, content string) testToolErrorPayload {
	t.Helper()
	var payload testToolErrorPayload
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("tool result is not structured JSON: %v\n%s", err, content)
	}
	return payload
}

func lastAssistantToolCalls(history []provider.Message) []provider.ToolCall {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
			return history[i].ToolCalls
		}
	}
	return nil
}

func toolResultContent(history []provider.Message, id string) (string, bool) {
	for _, message := range history {
		if message.Role == "tool" && message.ToolCallID == id {
			return messageText(message), true
		}
	}
	return "", false
}

func newToolCallServer(t *testing.T, first string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeSSE(w, first)
			return
		}
		writeSSE(w, finalResponse)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestAgentInvalidToolArgumentsReturnStructuredError(t *testing.T) {
	server, requests := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "record", Args: "{not json",
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	var failed []publicagent.ToolFailed
	instance.Subscribe(func(event publicagent.Event) {
		if failure, ok := event.(publicagent.ToolFailed); ok {
			failed = append(failed, failure)
		}
	})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}

	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
	if calls[0].Function.Arguments != "{}" {
		t.Fatalf("assistant arguments = %q, want sanitized {}", calls[0].Function.Arguments)
	}

	content, ok := toolResultContent(instance.History(), "call_1")
	if !ok {
		t.Fatal("tool result missing for call_1")
	}
	payload := parseToolErrorResult(t, content)
	if payload.OK || payload.Error.Code != toolCodeInvalidArguments {
		t.Fatalf("tool error = %+v", payload)
	}
	if payload.Error.Executed || !payload.Error.Retryable {
		t.Fatalf("tool error flags = %+v", payload.Error)
	}

	if len(failed) != 1 || failed[0].Code != toolCodeInvalidArguments || failed[0].Executed || !failed[0].Retryable {
		t.Fatalf("tool.failed events = %+v", failed)
	}
}

func TestAgentTruncatedToolArgumentsAreNotExecuted(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("length", toolCallSpec{
		ID: "call_1", Name: "record", Args: `{"path":"a"}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}

	content, ok := toolResultContent(instance.History(), "call_1")
	if !ok {
		t.Fatal("tool result missing for call_1")
	}
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeTruncatedArguments || payload.Error.Executed || !payload.Error.Retryable {
		t.Fatalf("tool error = %+v", payload)
	}

	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 1 || calls[0].Function.Arguments != "{}" {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
}

func TestAgentUnknownToolReturnsRecoverableError(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "nope", Args: `{}`,
	}))
	instance, _ := newTestAgent(t, server, Options{})

	var started []string
	var failed []publicagent.ToolFailed
	instance.Subscribe(func(event publicagent.Event) {
		switch value := event.(type) {
		case publicagent.ToolStarted:
			started = append(started, value.ToolName)
		case publicagent.ToolFailed:
			failed = append(failed, value)
		}
	})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(started) != 0 {
		t.Fatalf("unknown tool emitted tool.started: %+v", started)
	}
	if len(failed) != 1 || failed[0].Code != toolCodeUnknownTool || failed[0].Executed || failed[0].Retryable {
		t.Fatalf("tool.failed events = %+v", failed)
	}

	content, ok := toolResultContent(instance.History(), "call_1")
	if !ok {
		t.Fatal("tool result missing for call_1")
	}
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeUnknownTool || payload.Error.Executed || payload.Error.Retryable {
		t.Fatalf("tool error = %+v", payload)
	}
}

func TestAgentMultipleToolCallsCorrelatedAndNormalized(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls",
		toolCallSpec{ID: "dup", Name: "record", Args: `{"path":"a"}`},
		toolCallSpec{ID: "dup", Name: "nope", Args: `{}`},
	))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 1 {
		t.Fatalf("tool executed %d times, want 1", record.count.Load())
	}

	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 2 {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
	if calls[0].ID == "" || calls[1].ID == "" || calls[0].ID == calls[1].ID {
		t.Fatalf("tool call IDs were not normalized: %+v", calls)
	}
	if calls[0].Function.Name != "record" || calls[1].Function.Name != "nope" {
		t.Fatalf("tool call order = %+v", calls)
	}

	for _, call := range calls {
		if _, ok := toolResultContent(instance.History(), call.ID); !ok {
			t.Fatalf("missing tool result for %q", call.ID)
		}
	}

	content, _ := toolResultContent(instance.History(), calls[0].ID)
	if payload := parseToolErrorResult(t, content); !payload.OK || payload.Error.Code != "" {
		t.Fatalf("first tool result = %+v", payload)
	}
	content, _ = toolResultContent(instance.History(), calls[1].ID)
	if payload := parseToolErrorResult(t, content); payload.Error.Code != toolCodeUnknownTool {
		t.Fatalf("second tool result = %+v", payload)
	}
}

func TestAgentMissingToolCallIDIsNormalized(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		Name: "record", Args: `{}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 1 {
		t.Fatalf("tool executed %d times, want 1", record.count.Load())
	}

	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 1 || calls[0].ID == "" {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
	if _, ok := toolResultContent(instance.History(), calls[0].ID); !ok {
		t.Fatalf("missing tool result for generated ID %q", calls[0].ID)
	}
}

func TestAgentToolSchemaValidationRejectsMissingRequiredArgument(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "record", Args: `{}`,
	}))
	record := &recordingTool{required: []string{"path"}}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}
	content, _ := toolResultContent(instance.History(), "call_1")
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeInvalidArguments || payload.Error.Executed || !payload.Error.Retryable {
		t.Fatalf("tool error = %+v", payload)
	}
	if !strings.Contains(payload.Error.Message, "path") {
		t.Fatalf("error message = %q", payload.Error.Message)
	}
}

func TestAgentToolFailureIsPreservedAndNotRetried(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "record", Args: `{}`,
	}))
	record := &recordingTool{err: errors.New("disk full")}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	var failed []publicagent.ToolFailed
	instance.Subscribe(func(event publicagent.Event) {
		if failure, ok := event.(publicagent.ToolFailed); ok {
			failed = append(failed, failure)
		}
	})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 1 {
		t.Fatalf("tool executed %d times, want 1 (no automatic retry)", record.count.Load())
	}
	content, _ := toolResultContent(instance.History(), "call_1")
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeExecutionFailed || !payload.Error.Executed || payload.Error.Retryable {
		t.Fatalf("tool error = %+v", payload)
	}
	if len(failed) != 1 || failed[0].Code != toolCodeExecutionFailed || !failed[0].Executed {
		t.Fatalf("tool.failed events = %+v", failed)
	}
}

func TestAgentEmptyResponseDoesNotLoop(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	var finished []publicagent.TurnFinished
	instance.Subscribe(func(event publicagent.Event) {
		if turn, ok := event.(publicagent.TurnFinished); ok {
			finished = append(finished, turn)
		}
	})

	if err := instance.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if len(finished) != 1 || finished[0].ContentBytes != 0 || finished[0].Err != "" {
		t.Fatalf("turn.finished = %+v", finished)
	}
	for _, message := range instance.History() {
		if message.Role == "assistant" {
			t.Fatalf("empty response added an assistant message: %+v", message)
		}
	}
}

func TestAgentEmptyTruncatedResponseIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	err := instance.Run(context.Background(), "hello")
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Run() error = %v, want ErrEmptyResponse", err)
	}
}

func TestAgentTurnFinishedPrecedesToolExecutionAndSettle(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "record", Args: `{}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	var events []publicagent.EventType
	instance.Subscribe(func(event publicagent.Event) {
		events = append(events, event.Type())
	})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	firstTurnFinished := -1
	firstToolStarted := -1
	secondTurnStarted := -1
	settled := -1
	turnStarts := 0
	for index, eventType := range events {
		switch eventType {
		case publicagent.EventTurnFinished:
			if firstTurnFinished < 0 {
				firstTurnFinished = index
			}
		case publicagent.EventToolStarted:
			if firstToolStarted < 0 {
				firstToolStarted = index
			}
		case publicagent.EventTurnStarted:
			turnStarts++
			if turnStarts == 2 {
				secondTurnStarted = index
			}
		case publicagent.EventAgentFinished:
			settled = index
		}
	}
	if firstTurnFinished < 0 || firstToolStarted < 0 || secondTurnStarted < 0 || settled < 0 {
		t.Fatalf("missing lifecycle events in %v", events)
	}
	if firstTurnFinished >= firstToolStarted || firstToolStarted >= secondTurnStarted || secondTurnStarted >= settled {
		t.Fatalf("event order = %v", events)
	}
}

func TestAgentContextOverflowCompactsAndRetries(t *testing.T) {
	var mainRequests atomic.Int32
	var summaryRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools    []provider.Tool    `json:"tools"`
			Messages []provider.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if len(body.Tools) == 0 {
			summaryRequests.Add(1)
			writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"earlier work summary"},"finish_reason":"stop"}]}`)
			return
		}

		if mainRequests.Add(1) == 1 {
			http.Error(w, "maximum context length exceeded", http.StatusBadRequest)
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{
		ContextWindow:    4096,
		ReserveTokens:    1024,
		CompactAt:        0.9,
		KeepRecentTokens: 256,
		SummaryMaxTokens: 128,
	})

	history := []provider.Message{
		{Role: "system", Content: "You are Iris."},
		{Role: "user", Content: "first request"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second request"},
		{Role: "assistant", Content: "second answer"},
	}
	if err := instance.LoadHistory(history); err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	var retried []publicagent.TurnRetried
	var finished []publicagent.TurnFinished
	var forcedCompaction bool
	instance.Subscribe(func(event publicagent.Event) {
		switch value := event.(type) {
		case publicagent.TurnRetried:
			retried = append(retried, value)
		case publicagent.TurnFinished:
			finished = append(finished, value)
		case publicagent.ContextCompactionFinished:
			if value.Forced {
				forcedCompaction = true
			}
		}
	})

	if err := instance.Run(context.Background(), "continue"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if mainRequests.Load() != 2 {
		t.Fatalf("main requests = %d, want 2", mainRequests.Load())
	}
	if summaryRequests.Load() != 1 {
		t.Fatalf("summary requests = %d, want 1", summaryRequests.Load())
	}
	if len(retried) != 1 || retried[0].Reason != "context_overflow" || retried[0].NextAttempt != 2 {
		t.Fatalf("turn.retried = %+v", retried)
	}
	if len(finished) != 1 || finished[0].Attempts != 2 {
		t.Fatalf("turn.finished = %+v", finished)
	}
	if !forcedCompaction {
		t.Fatal("forced compaction was not reported")
	}
	if len(instance.History()) == 0 || messageText(instance.History()[len(instance.History())-1]) != "recovered" {
		t.Fatalf("last message = %+v", instance.History()[len(instance.History())-1])
	}
}

func TestAgentProviderDisconnectFailsCleanly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})

	var finished *publicagent.TurnFinished
	instance.Subscribe(func(event publicagent.Event) {
		if turn, ok := event.(publicagent.TurnFinished); ok {
			finished = &turn
		}
	})

	err := instance.Run(context.Background(), "hello")
	if !errors.Is(err, provider.ErrStreamInterrupted) {
		t.Fatalf("Run() error = %v, want ErrStreamInterrupted", err)
	}
	if finished == nil || finished.Err == "" {
		t.Fatalf("turn.finished = %+v, want an error", finished)
	}
	for _, message := range instance.History() {
		if message.Role == "assistant" {
			t.Fatalf("partial response was committed to history: %+v", message)
		}
	}
}

func TestAgentContextOverflowWithoutRecoveryFailsWithPublicSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not reach the provider", http.StatusInternalServerError)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{
		ContextWindow:    1024,
		ReserveTokens:    256,
		CompactAt:        0.9,
		KeepRecentTokens: 128,
		SummaryMaxTokens: 64,
	})

	err := instance.Run(context.Background(), strings.Repeat("x", 20_000))
	if !errors.Is(err, publicagent.ErrContextOverflow) {
		t.Fatalf("Run() error = %v, want ErrContextOverflow", err)
	}
}

func TestAgentEmitsDeltasForNonStreamingProvider(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{Mode: policy.ModeWorkspaceWrite})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	fake := provider.ResponseFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{
			Content:          "hello",
			ReasoningContent: "thinking",
			FinishReason:     "stop",
		}, nil
	})

	instance, err := NewAgent(fake, pol)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	var content, reasoning strings.Builder
	instance.Subscribe(func(event publicagent.Event) {
		switch value := event.(type) {
		case publicagent.AssistantDelta:
			content.WriteString(value.Text)
		case publicagent.ReasoningDelta:
			reasoning.WriteString(value.Text)
		}
	})

	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if content.String() != "hello" || reasoning.String() != "thinking" {
		t.Fatalf("streamed content = %q reasoning = %q", content.String(), reasoning.String())
	}
}

func TestAgentMissingToolNameReturnsStructuredError(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "", Args: `{"path":"a"}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}

	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 1 || calls[0].Function.Name != "invalid_tool" {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
	content, _ := toolResultContent(instance.History(), "call_1")
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeInvalidCall || payload.Error.Executed || payload.Error.Retryable {
		t.Fatalf("tool error = %+v", payload)
	}
}

func TestAgentContentFilteredToolCallsAreNotExecuted(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("content_filter", toolCallSpec{
		ID: "call_1", Name: "record", Args: `{}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}
	content, _ := toolResultContent(instance.History(), "call_1")
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeInvalidCall || payload.Error.Executed {
		t.Fatalf("tool error = %+v", payload)
	}
	if !strings.Contains(payload.Error.Message, "content filter") {
		t.Fatalf("error message = %q", payload.Error.Message)
	}
}

func TestAgentUnsafeToolNameIsSanitized(t *testing.T) {
	server, _ := newToolCallServer(t, sseToolCalls("tool_calls", toolCallSpec{
		ID: "call_1", Name: "foo.bar", Args: `{}`,
	}))
	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := lastAssistantToolCalls(instance.History())
	if len(calls) != 1 || calls[0].Function.Name != "invalid_tool" {
		t.Fatalf("assistant tool calls = %+v", calls)
	}
	content, _ := toolResultContent(instance.History(), "call_1")
	payload := parseToolErrorResult(t, content)
	if payload.Error.Code != toolCodeInvalidCall || payload.Error.Executed {
		t.Fatalf("tool error = %+v", payload)
	}
}

func TestAgentUsesStreamedContentWhenTerminalResponseIsEmpty(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{Mode: policy.ModeWorkspaceWrite})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	fake := provider.ProviderFunc(func(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
		events := make(chan provider.StreamEvent, 3)
		events <- provider.DeltaEvent{Delta: provider.Delta{Content: "he"}}
		events <- provider.DeltaEvent{Delta: provider.Delta{Content: "llo"}}
		events <- provider.DoneEvent{Response: provider.Response{FinishReason: "stop"}}
		close(events)
		return events, nil
	})

	instance, err := NewAgent(fake, pol)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	var content strings.Builder
	var finished []publicagent.TurnFinished
	instance.Subscribe(func(event publicagent.Event) {
		switch value := event.(type) {
		case publicagent.AssistantDelta:
			content.WriteString(value.Text)
		case publicagent.TurnFinished:
			finished = append(finished, value)
		}
	})

	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	history := instance.History()
	if got := messageText(history[len(history)-1]); got != "hello" {
		t.Fatalf("assistant history content = %q, want hello", got)
	}
	if content.String() != "hello" {
		t.Fatalf("streamed content = %q, want hello exactly once", content.String())
	}
	if len(finished) != 1 || finished[0].ContentBytes != 5 {
		t.Fatalf("turn.finished = %+v", finished)
	}
}

func TestAgentEmitsMissingSuffixWhenTerminalResponseIsFuller(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{Mode: policy.ModeWorkspaceWrite})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	fake := provider.ProviderFunc(func(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
		events := make(chan provider.StreamEvent, 2)
		events <- provider.DeltaEvent{Delta: provider.Delta{Content: "he"}}
		events <- provider.DoneEvent{Response: provider.Response{Content: "hello", FinishReason: "stop"}}
		close(events)
		return events, nil
	})

	instance, err := NewAgent(fake, pol)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	var content strings.Builder
	instance.Subscribe(func(event publicagent.Event) {
		if delta, ok := event.(publicagent.AssistantDelta); ok {
			content.WriteString(delta.Text)
		}
	})

	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := content.String(); got != "hello" {
		t.Fatalf("streamed content = %q, want hello", got)
	}
	history := instance.History()
	if got := messageText(history[len(history)-1]); got != "hello" {
		t.Fatalf("assistant history content = %q, want hello", got)
	}
}

type durableLog struct {
	mu      sync.Mutex
	events  []string
	records []publicagent.AssistantRecord
	failOn  string
}

func (l *durableLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *durableLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *durableLog) fail(operation string) error {
	if l.failOn == operation {
		return errors.New("recorder unavailable")
	}
	return nil
}

type durableRecorder struct {
	log *durableLog
}

func (r *durableRecorder) RecordUser(text string) error {
	r.log.add("record-user")
	return r.log.fail("user")
}

func (r *durableRecorder) RecordAssistant(record publicagent.AssistantRecord) error {
	r.log.mu.Lock()
	r.log.records = append(r.log.records, record)
	r.log.mu.Unlock()
	r.log.add("record-assistant")
	return r.log.fail("assistant")
}

func (r *durableRecorder) RecordToolResult(record publicagent.ToolResultRecord) error {
	r.log.add("record-tool-result")
	return r.log.fail("tool-result")
}

func TestAgentRecordsDurabilityPointsInOrder(t *testing.T) {
	log := &durableLog{}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		log.add("llm-request")
		if requests.Add(1) == 1 {
			writeSSE(w, sseToolCalls("tool_calls", toolCallSpec{ID: "call_1", Name: "record", Args: `{}`}))
			return
		}
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	record := &recordingTool{onExecute: func() { log.add("tool-execute") }}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})
	instance.SetRecorder(&durableRecorder{log: log})

	if err := instance.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if record.count.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1", record.count.Load())
	}

	events := log.snapshot()
	index := func(name string, from int) int {
		for i := from; i < len(events); i++ {
			if events[i] == name {
				return i
			}
		}
		return -1
	}
	user := index("record-user", 0)
	firstRequest := index("llm-request", 0)
	assistant := index("record-assistant", 0)
	tool := index("tool-execute", 0)
	result := index("record-tool-result", 0)
	secondRequest := index("llm-request", tool)
	finalAssistant := index("record-assistant", secondRequest)
	if user < 0 || firstRequest < 0 || assistant < 0 || tool < 0 || result < 0 || secondRequest < 0 || finalAssistant < 0 {
		t.Fatalf("missing durability events: %v", events)
	}
	if user >= firstRequest || firstRequest >= assistant || assistant >= tool ||
		tool >= result || result >= secondRequest || secondRequest >= finalAssistant {
		t.Fatalf("durability event order = %v", events)
	}
}

func TestRecorderFailureBeforeToolExecutionPreventsSideEffects(t *testing.T) {
	log := &durableLog{failOn: "assistant"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, sseToolCalls("tool_calls", toolCallSpec{ID: "call_1", Name: "record", Args: `{}`}))
	}))
	defer server.Close()

	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})
	instance.SetRecorder(&durableRecorder{log: log})

	err := instance.Run(context.Background(), "do it")
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("Run() error = %v, want ErrPersistence", err)
	}
	if record.count.Load() != 0 {
		t.Fatalf("tool executed %d times, want 0", record.count.Load())
	}
	for _, message := range instance.History() {
		if message.Role == "assistant" {
			t.Fatalf("unpersisted assistant message remained in history: %+v", message)
		}
	}
}

func TestRecorderFailureAfterToolExecutionAbortsRun(t *testing.T) {
	log := &durableLog{failOn: "tool-result"}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeSSE(w, sseToolCalls("tool_calls", toolCallSpec{ID: "call_1", Name: "record", Args: `{}`}))
	}))
	defer server.Close()

	record := &recordingTool{}
	instance, _ := newTestAgent(t, server, Options{Tools: []apitype.Tool{record}})
	instance.SetRecorder(&durableRecorder{log: log})

	err := instance.Run(context.Background(), "do it")
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("Run() error = %v, want ErrPersistence", err)
	}
	if record.count.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1 (execution already happened)", record.count.Load())
	}
	if requests.Load() != 1 {
		t.Fatalf("LLM requests = %d, want 1 (abort before another turn)", requests.Load())
	}
	history := instance.History()
	foundTool := false
	for _, message := range history {
		if message.Role == "tool" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("executed tool result missing from history: %+v", history)
	}
}

func TestRecorderFailureOnUserInputPreventsModelCall(t *testing.T) {
	log := &durableLog{failOn: "user"}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeSSE(w, finalResponse)
	}))
	defer server.Close()

	instance, _ := newTestAgent(t, server, Options{})
	instance.SetRecorder(&durableRecorder{log: log})

	err := instance.Run(context.Background(), "do it")
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("Run() error = %v, want ErrPersistence", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("LLM requests = %d, want 0", requests.Load())
	}
	for _, message := range instance.History() {
		if message.Role == "user" {
			t.Fatalf("unpersisted user message remained in history: %+v", message)
		}
	}
}
