package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	publicagent "github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/internal/policy"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/resource"
)

func TestAgentCompactsContextButPreservesFullHistory(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	var (
		mu            sync.Mutex
		summaryCalls  int
		mainCalls     int
		mainMessages  []provider.Message
		mainMaxTokens int
	)

	fake := provider.ProviderFunc(func(
		_ context.Context,
		request provider.Request,
		_ func(provider.Delta),
	) (provider.Response, error) {
		mu.Lock()
		defer mu.Unlock()

		// Summarization requests are sent without tools.
		if len(request.Tools) == 0 {
			summaryCalls++
			return provider.Response{
				Content: "EARLIER SUMMARY: decisions and tool outcomes",
				Usage:   provider.Usage{PromptTokens: 30, CompletionTokens: 10},
			}, nil
		}

		mainCalls++
		mainMessages = append([]provider.Message(nil), request.Messages...)
		mainMaxTokens = request.MaxTokens
		return provider.Response{Content: "done", Usage: provider.Usage{PromptTokens: 100, CompletionTokens: 5}}, nil
	})

	instance, err := NewAgent(fake, pol, Options{
		Model:            "test",
		ContextWindow:    4096,
		ReserveTokens:    512,
		CompactAt:        0.8,
		KeepRecentTokens: 600,
		SummaryMaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	const marker = "VERY_OLD_UNIQUE_REQUEST"
	history := make([]provider.Message, 0, 80)
	for i := 0; i < 40; i++ {
		content := "older context detail " + strings.Repeat("detail ", 20)
		if i == 0 {
			content = marker + " " + content
		}
		history = append(history,
			provider.Message{
				Role:    "user",
				Content: []provider.Content{{Type: "text", Text: content}},
			},
			provider.Message{
				Role:    "assistant",
				Content: "older answer " + strings.Repeat("detail ", 20),
			},
		)
	}
	if err := instance.LoadHistory(history); err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	var (
		compactionStarts   int
		compactionFinishes int
		compactions        []publicagent.ContextCompactionFinished
	)
	instance.Subscribe(func(event publicagent.Event) {
		switch compacted := event.(type) {
		case publicagent.ContextCompactionStarted:
			compactionStarts++
		case publicagent.ContextCompactionFinished:
			compactionFinishes++
			compactions = append(compactions, compacted)
		}
	})

	if err := instance.Run(context.Background(), "current request"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if summaryCalls == 0 {
		t.Fatal("expected a summarization call")
	}
	if mainCalls != 1 {
		t.Fatalf("main calls = %d, want 1", mainCalls)
	}
	if mainMaxTokens != 512 {
		t.Fatalf("main max tokens = %d, want 512", mainMaxTokens)
	}
	if len(mainMessages) == 0 || len(mainMessages) >= len(history)+2 {
		t.Fatalf("main prompt length = %d, full history = %d", len(mainMessages), len(history)+2)
	}

	foundSummary := false
	foundOldMarker := false
	for _, message := range mainMessages {
		text := messageTextForTest(message.Content)
		if strings.Contains(text, "EARLIER SUMMARY") {
			foundSummary = true
		}
		if strings.Contains(text, marker) {
			foundOldMarker = true
		}
	}
	if !foundSummary {
		t.Fatal("compacted prompt does not contain the summary")
	}
	if foundOldMarker {
		t.Fatal("compacted prompt still contains the old marker")
	}
	if compactionStarts != 1 || compactionFinishes != 1 {
		t.Fatalf("compaction events = %d started, %d finished; want 1 each", compactionStarts, compactionFinishes)
	}
	if compactions[0].SummarizedMessages == 0 {
		t.Fatalf("compaction event = %+v", compactions[0])
	}

	// The session-facing history must remain the uncompressed conversation.
	full := instance.History()
	if len(full) != len(history)+3 { // system + history + current user + assistant
		t.Fatalf("full history length = %d, want %d", len(full), len(history)+3)
	}
	foundFullMarker := false
	for _, message := range full {
		if strings.Contains(messageTextForTest(message.Content), marker) {
			foundFullMarker = true
		}
	}
	if !foundFullMarker {
		t.Fatal("full history lost the original uncompressed messages")
	}
}

func TestAgentRecoversFromProviderContextWindowError(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	var (
		mu           sync.Mutex
		mainCalls    int
		summaryCalls int
	)

	fake := provider.ProviderFunc(func(
		_ context.Context,
		request provider.Request,
		_ func(provider.Delta),
	) (provider.Response, error) {
		mu.Lock()
		defer mu.Unlock()

		if len(request.Tools) == 0 {
			summaryCalls++
			return provider.Response{Content: "summary after rejection"}, nil
		}

		mainCalls++
		if mainCalls == 1 {
			return provider.Response{}, fmt.Errorf("%w: maximum context length exceeded", provider.ErrContextWindow)
		}
		return provider.Response{Content: "recovered", Usage: provider.Usage{PromptTokens: 80, CompletionTokens: 4}}, nil
	})

	instance, err := NewAgent(fake, pol, Options{
		Model:            "test",
		ContextWindow:    4096,
		ReserveTokens:    512,
		CompactAt:        0.9,
		KeepRecentTokens: 3000,
		SummaryMaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	history := make([]provider.Message, 0, 30)
	for i := 0; i < 15; i++ {
		history = append(history,
			provider.Message{
				Role:    "user",
				Content: []provider.Content{{Type: "text", Text: "older request " + strings.Repeat("detail ", 20)}},
			},
			provider.Message{Role: "assistant", Content: "older answer " + strings.Repeat("detail ", 20)},
		)
	}
	if err := instance.LoadHistory(history); err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	if err := instance.Run(context.Background(), "current request"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if mainCalls != 2 {
		t.Fatalf("main calls = %d, want 2 (initial + retry)", mainCalls)
	}
	if summaryCalls == 0 {
		t.Fatal("expected compaction during recovery")
	}
}

func TestContextWindowErrorClassifier(t *testing.T) {
	if !provider.IsContextWindowError(provider.ErrContextWindow) {
		t.Fatal("sentinel not classified")
	}
	if !provider.IsContextWindowError(errors.New("This model's maximum context length is 8192 tokens")) {
		t.Fatal("message not classified")
	}
	if provider.IsContextWindowError(errors.New("network unavailable")) {
		t.Fatal("unrelated error classified")
	}
}

func messageTextForTest(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []provider.Content:
		var builder strings.Builder
		for _, part := range value {
			builder.WriteString(part.Text)
		}
		return builder.String()
	default:
		return fmt.Sprint(value)
	}
}

func TestAgentIncludesResourcesInSystemPrompt(t *testing.T) {
	pol, err := policy.New(t.TempDir(), policy.Options{})
	if err != nil {
		t.Fatalf("policy.New() error = %v", err)
	}

	var systemPrompt string
	fake := provider.ProviderFunc(func(
		_ context.Context,
		request provider.Request,
		_ func(provider.Delta),
	) (provider.Response, error) {
		if len(request.Messages) > 0 {
			systemPrompt = messageTextForTest(request.Messages[0].Content)
		}
		return provider.Response{Content: "ok"}, nil
	})

	resources := &resource.Resources{
		SystemPrompt: "CUSTOM_SYSTEM_INSTRUCTION",
		Instructions: []resource.Instruction{
			{Scope: resource.ScopeProject, Content: "PROJECT_AGENTS_INSTRUCTION"},
		},
	}
	instance, err := NewAgent(fake, pol, Options{Resources: resources})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	defer instance.Close()

	if err := instance.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, want := range []string{
		"CUSTOM_SYSTEM_INSTRUCTION",
		"PROJECT_AGENTS_INSTRUCTION",
		"Instruction resources (AGENTS.md)",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
}
