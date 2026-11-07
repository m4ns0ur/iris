package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/m4ns0ur/iris/provider"
)

func testConfig() Config {
	return Config{
		Window:           1024,
		ReserveTokens:    128,
		CompactAt:        0.8,
		KeepRecentTokens: 200,
		SummaryMaxTokens: 128,
	}
}

func textMessage(role, text string) provider.Message {
	return provider.Message{Role: role, Content: text}
}

func userTextMessage(text string) provider.Message {
	return provider.Message{
		Role:    "user",
		Content: []provider.Content{{Type: "text", Text: text}},
	}
}

func longHistory(turns int) []provider.Message {
	history := []provider.Message{textMessage("system", "You are Iris. Follow project instructions.")}
	for i := 0; i < turns; i++ {
		history = append(history,
			userTextMessage("user request about a long coding task with context "+strings.Repeat("detail ", 30)),
			textMessage("assistant", "assistant response with decisions and details "+strings.Repeat("detail ", 30)),
		)
	}
	return history
}

type recordingObserver struct {
	starts   int
	finishes int
	during   bool
	last     FinishInfo
}

func (o *recordingObserver) CompactionStarted(StartInfo) {
	o.starts++
	o.during = true
}

func (o *recordingObserver) CompactionFinished(info FinishInfo) {
	o.finishes++
	o.during = false
	o.last = info
}

func TestBuildKeepsSmallHistoryUnchanged(t *testing.T) {
	manager := New(testConfig())
	observer := &recordingObserver{}
	manager.SetObserver(observer)
	history := []provider.Message{
		textMessage("system", "system"),
		userTextMessage("hello"),
		textMessage("assistant", "hi"),
	}

	view, compaction, err := manager.Build(context.Background(), history, nil, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if compaction != nil {
		t.Fatalf("compaction = %+v, want nil", compaction)
	}
	if observer.starts != 0 || observer.finishes != 0 {
		t.Fatalf("observer = %d starts, %d finishes; want none", observer.starts, observer.finishes)
	}
	if len(view) != len(history) {
		t.Fatalf("view length = %d, want %d", len(view), len(history))
	}

	stats := manager.Stats()
	if stats.EstimatedPromptTokens == 0 || stats.Window != 1024 || stats.ReserveTokens != 128 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestBuildCompactsOlderHistory(t *testing.T) {
	manager := New(testConfig())
	observer := &recordingObserver{}
	manager.SetObserver(observer)
	history := longHistory(12)
	original := append([]provider.Message(nil), history...)

	var transcript string
	view, compaction, err := manager.Build(
		context.Background(),
		history,
		nil,
		func(_ context.Context, text string, maxTokens int) (SummarizeResult, error) {
			transcript = text
			if !observer.during {
				t.Fatal("summarizer ran outside the compaction start/finish window")
			}
			if maxTokens != 128 {
				t.Fatalf("summary max tokens = %d, want 128", maxTokens)
			}
			return SummarizeResult{
				Summary: "Earlier: the user worked on a coding task. Decision: keep the API stable.",
				Usage:   provider.Usage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if observer.starts != 1 || observer.finishes != 1 {
		t.Fatalf("observer = %d starts, %d finishes; want 1 each", observer.starts, observer.finishes)
	}
	if observer.last.Compaction == nil {
		t.Fatal("observer finish did not include compaction details")
	}
	if compaction == nil {
		t.Fatal("expected compaction")
	}
	if compaction.SummarizedMessages == 0 {
		t.Fatalf("compaction = %+v", compaction)
	}
	if compaction.SummaryPromptTokens != 50 || compaction.SummaryCompletionTokens != 20 {
		t.Fatalf("summary usage = %+v", compaction)
	}
	if !strings.Contains(transcript, "coding task") {
		t.Fatalf("summary transcript missing history: %q", transcript)
	}
	if len(view) >= len(history) {
		t.Fatalf("view length = %d, original = %d", len(view), len(history))
	}
	if view[0].Role != "system" || !strings.Contains(textContent(view[0].Content), "Compacted conversation summary") {
		t.Fatalf("system message = %+v", view[0])
	}
	if !strings.Contains(textContent(view[0].Content), "You are Iris") {
		t.Fatal("original system prompt was not preserved")
	}

	last := view[len(view)-1]
	if last.Role != "assistant" {
		t.Fatalf("last view message = %q, want assistant", last.Role)
	}

	if len(history) != len(original) {
		t.Fatalf("original history length changed: %d -> %d", len(original), len(history))
	}
	for i := range original {
		if textContent(original[i].Content) != textContent(history[i].Content) {
			t.Fatalf("original history message %d changed", i)
		}
	}

	stats := manager.Stats()
	if stats.CompactedThrough == 0 || stats.LastCompaction == nil {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.SummaryTokens == 0 {
		t.Fatalf("summary tokens = %d", stats.SummaryTokens)
	}
}

func TestBuildFallsBackWhenSummarizerFails(t *testing.T) {
	manager := New(testConfig())
	history := longHistory(8)

	view, compaction, err := manager.Build(
		context.Background(),
		history,
		nil,
		func(context.Context, string, int) (SummarizeResult, error) {
			return SummarizeResult{}, errors.New("summarizer unavailable")
		},
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if compaction == nil || !compaction.SummaryFallback {
		t.Fatalf("compaction = %+v, want fallback", compaction)
	}
	if !strings.Contains(textContent(view[0].Content), "automatic fallback") {
		t.Fatalf("fallback summary missing: %q", textContent(view[0].Content))
	}
}

func TestForceCompactsAndReportsForced(t *testing.T) {
	manager := New(testConfig())
	history := longHistory(5)

	view, compaction, err := manager.Force(
		context.Background(),
		history,
		nil,
		func(context.Context, string, int) (SummarizeResult, error) {
			return SummarizeResult{Summary: "forced summary"}, nil
		},
	)
	if err != nil {
		t.Fatalf("Force() error = %v", err)
	}
	if compaction == nil || !compaction.Forced {
		t.Fatalf("compaction = %+v, want forced", compaction)
	}
	if !strings.Contains(textContent(view[0].Content), "forced summary") {
		t.Fatalf("forced summary missing: %q", textContent(view[0].Content))
	}
}

func TestBuildPreservesMultipleLeadingSystemMessages(t *testing.T) {
	config := testConfig()
	config.Window = 900
	config.ReserveTokens = 100
	config.KeepRecentTokens = 200
	manager := New(config)

	history := []provider.Message{
		textMessage("system", "base system prompt"),
		textMessage("system", "project instructions: use tabs"),
		userTextMessage("old request " + strings.Repeat("detail ", 300)),
		textMessage("assistant", "old answer "+strings.Repeat("detail ", 300)),
		userTextMessage("current request"),
	}

	view, compaction, err := manager.Build(
		context.Background(),
		history,
		nil,
		func(context.Context, string, int) (SummarizeResult, error) {
			return SummarizeResult{Summary: "summary"}, nil
		},
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if compaction == nil {
		t.Fatal("expected compaction")
	}
	if len(view) < 2 || textContent(view[0].Content) != "base system prompt" {
		t.Fatalf("system prompt 0 = %+v", view[0])
	}
	if !strings.Contains(textContent(view[1].Content), "use tabs") ||
		!strings.Contains(textContent(view[1].Content), "summary") {
		t.Fatalf("project instructions/summary = %q", textContent(view[1].Content))
	}
}

func TestBuildTrimsHugeToolResult(t *testing.T) {
	config := Config{
		Window:           1200,
		ReserveTokens:    150,
		CompactAt:        0.8,
		KeepRecentTokens: 200,
		SummaryMaxTokens: 100,
	}
	manager := New(config)

	history := []provider.Message{
		textMessage("system", "system"),
		userTextMessage("run the tool"),
		{
			Role: "assistant",
			ToolCalls: []provider.ToolCall{{
				ID:       "call-1",
				Function: provider.FunctionCall{Name: "shell", Arguments: `{}`},
			}},
		},
		{
			Role:       "tool",
			Content:    strings.Repeat("very long tool output ", 500),
			ToolCallID: "call-1",
		},
	}

	view, compaction, err := manager.Build(context.Background(), history, nil, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if compaction == nil || compaction.TrimmedToolResults == 0 {
		t.Fatalf("compaction = %+v, want tool trimming", compaction)
	}
	tool := view[len(view)-1]
	if tool.Role != "tool" || !strings.Contains(textContent(tool.Content), "truncated by context manager") {
		t.Fatalf("tool result = %+v", tool)
	}
}

func TestBuildReportsOverflowWhenPromptCannotFit(t *testing.T) {
	config := Config{
		Window:           1200,
		ReserveTokens:    150,
		CompactAt:        0.8,
		KeepRecentTokens: 200,
		SummaryMaxTokens: 100,
	}
	manager := New(config)

	history := []provider.Message{
		textMessage("system", "system"),
		userTextMessage(strings.Repeat("huge current input ", 2000)),
	}

	_, _, err := manager.Build(context.Background(), history, nil, nil)
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("Build() error = %v, want ErrContextOverflow", err)
	}
}

func TestObserveUsageTracksReportedAndEstimatedTokens(t *testing.T) {
	manager := New(testConfig())
	history := []provider.Message{
		textMessage("system", "system"),
		userTextMessage("hello"),
	}

	if _, _, err := manager.Build(context.Background(), history, nil, nil); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	manager.ObserveUsage(provider.Usage{
		PromptTokens:     120,
		CompletionTokens: 20,
		TotalTokens:      140,
	})

	stats := manager.Stats()
	if stats.ReportedPromptTokens != 120 || stats.ReportedCompletionTokens != 20 {
		t.Fatalf("reported stats = %+v", stats)
	}
	if stats.EstimatedPromptTokens == 0 {
		t.Fatalf("estimated stats = %+v", stats)
	}
}

func TestResetClearsCompactionState(t *testing.T) {
	manager := New(testConfig())
	history := longHistory(10)

	if _, compaction, err := manager.Build(
		context.Background(),
		history,
		nil,
		func(context.Context, string, int) (SummarizeResult, error) {
			return SummarizeResult{Summary: "summary"}, nil
		},
	); err != nil || compaction == nil {
		t.Fatalf("Build() = %v, %+v", err, compaction)
	}

	manager.Reset()
	stats := manager.Stats()
	if stats.CompactedThrough != 0 || stats.SummaryTokens != 0 || stats.LastCompaction != nil {
		t.Fatalf("stats after reset = %+v", stats)
	}

	view, compaction, err := manager.Build(
		context.Background(),
		[]provider.Message{
			textMessage("system", "system"),
			userTextMessage("fresh"),
		},
		nil,
		nil,
	)
	if err != nil || compaction != nil || len(view) != 2 {
		t.Fatalf("Build() after reset = %v, %+v, %d", err, compaction, len(view))
	}
}

func TestConfigNormalizesTinyWindow(t *testing.T) {
	manager := New(Config{Window: 512})
	stats := manager.Stats()
	if stats.Window != 1024 {
		t.Fatalf("window = %d, want 1024", stats.Window)
	}
	if stats.ReserveTokens <= 0 || stats.ReserveTokens >= stats.Window {
		t.Fatalf("reserve = %d", stats.ReserveTokens)
	}
	if stats.PromptBudget <= 0 {
		t.Fatalf("prompt budget = %d", stats.PromptBudget)
	}
}
