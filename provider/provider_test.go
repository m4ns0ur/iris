package provider

import (
	"context"
	"errors"
	"testing"
)

func eventsProvider(events ...StreamEvent) ProviderFunc {
	return func(context.Context, Request) (<-chan StreamEvent, error) {
		channel := make(chan StreamEvent, len(events))
		for _, event := range events {
			channel <- event
		}
		close(channel)
		return channel, nil
	}
}

func TestCollectReturnsFinalResponse(t *testing.T) {
	p := eventsProvider(
		DeltaEvent{Delta: Delta{Content: "he"}},
		DeltaEvent{Delta: Delta{Content: "llo"}},
		DoneEvent{Response: Response{Content: "hello", Usage: Usage{TotalTokens: 3}}},
	)

	response, err := Collect(context.Background(), p, Request{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.Content != "hello" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response = %+v", response)
	}
}

func TestCollectReturnsStreamError(t *testing.T) {
	want := errors.New("stream failed")
	p := eventsProvider(ErrorEvent{Err: want})

	_, err := Collect(context.Background(), p, Request{})
	if !errors.Is(err, want) {
		t.Fatalf("Collect() error = %v, want %v", err, want)
	}
}

func TestCollectRejectsStreamWithoutTerminalEvent(t *testing.T) {
	p := eventsProvider(DeltaEvent{Delta: Delta{Content: "partial"}})
	_, err := Collect(context.Background(), p, Request{})
	if !errors.Is(err, ErrStreamInterrupted) {
		t.Fatalf("Collect() error = %v, want ErrStreamInterrupted", err)
	}
}

func TestResponseFuncStreamsDoneEvent(t *testing.T) {
	p := ResponseFunc(func(context.Context, Request) (Response, error) {
		return Response{Content: "ok"}, nil
	})
	response, err := Collect(context.Background(), p, Request{})
	if err != nil || response.Content != "ok" {
		t.Fatalf("Collect() = %+v, %v", response, err)
	}
}
