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
