package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/m4ns0ur/iris/agent"
	"github.com/m4ns0ur/iris/provider"
)

func TestRuntimeCreatesAgentAndEmitsEvents(t *testing.T) {
	fake := provider.ProviderFunc(func(
		_ context.Context,
		_ provider.Request,
		onDelta func(provider.Delta),
	) (provider.Response, error) {
		if onDelta != nil {
			onDelta(provider.Delta{Content: "hello"})
		}
		return provider.Response{
			Content:      "hello",
			FinishReason: "stop",
			Usage:        provider.Usage{TotalTokens: 3},
		}, nil
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
	fake := provider.ProviderFunc(func(
		context.Context,
		provider.Request,
		func(provider.Delta),
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
