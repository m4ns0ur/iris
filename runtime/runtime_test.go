package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
	"github.com/m4ns0ur/iris/resource"
)

func TestRuntimeCreatesAgentAndEmitsEvents(t *testing.T) {
	fake := provider.ProviderFunc(func(
		_ context.Context,
		_ provider.Request,
	) (<-chan provider.StreamEvent, error) {
		events := make(chan provider.StreamEvent, 2)
		events <- provider.DeltaEvent{Delta: provider.Delta{Content: "hello"}}
		events <- provider.DoneEvent{Response: provider.Response{
			Content:      "hello",
			FinishReason: "stop",
			Usage:        provider.Usage{TotalTokens: 3},
		}}
		close(events)
		return events, nil
	})

	rt, err := New(Options{
		Workspace: t.TempDir(),
		Provider:  fake,
		Security:  Security{Mode: "workspace-write"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	instance, err := rt.NewAgent()
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	seen := make(map[agent.EventType]bool)
	instance.Subscribe(func(event agent.Event) {
		seen[event.Type()] = true
	})

	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, eventType := range []agent.EventType{
		agent.EventAgentStarted,
		agent.EventAssistantDelta,
		agent.EventUsage,
		agent.EventAgentFinished,
	} {
		if !seen[eventType] {
			t.Fatalf("event %q was not emitted; seen = %v", eventType, seen)
		}
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRuntimeRejectsNilProvider(t *testing.T) {
	_, err := New(Options{Workspace: t.TempDir()})
	if err == nil {
		t.Fatal("New() error = nil, want provider error")
	}
}

func TestRuntimeAgentIsClosedWithRuntime(t *testing.T) {
	fake := provider.ResponseFunc(func(
		context.Context,
		provider.Request,
	) (provider.Response, error) {
		return provider.Response{Content: "ok", FinishReason: "stop"}, nil
	})

	rt, err := New(Options{Workspace: t.TempDir(), Provider: fake})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	instance, err := rt.NewAgent()
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	if err := rt.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := instance.Run(context.Background(), "after close"); !errors.Is(err, agent.ErrAgentClosed) {
		t.Fatalf("Run() after runtime Close error = %v, want ErrAgentClosed", err)
	}
}

func TestRuntimePassesResourcesToAgent(t *testing.T) {
	var systemPrompt string
	fake := provider.ResponseFunc(func(
		_ context.Context,
		request provider.Request,
	) (provider.Response, error) {
		if len(request.Messages) > 0 {
			switch content := request.Messages[0].Content.(type) {
			case string:
				systemPrompt = content
			case []provider.Content:
				for _, part := range content {
					systemPrompt += part.Text
				}
			}
		}
		return provider.Response{Content: "ok", FinishReason: "stop"}, nil
	})

	rt, err := New(Options{
		Workspace: t.TempDir(),
		Provider:  fake,
		Resources: &resource.Resources{
			SystemPrompt: "RUNTIME_CUSTOM_SYSTEM",
			Instructions: []resource.Instruction{{Scope: resource.ScopeProject, Content: "RUNTIME_AGENTS"}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	instance, err := rt.NewAgent()
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if err := instance.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(systemPrompt, "RUNTIME_CUSTOM_SYSTEM") ||
		!strings.Contains(systemPrompt, "RUNTIME_AGENTS") {
		t.Fatalf("system prompt missing resources:\n%s", systemPrompt)
	}
}
