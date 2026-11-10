// Package provider defines the stable interface between the Iris agent
// runtime and LLM backends.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrContextWindow marks provider errors caused by exceeding the model's
// context window. Providers should wrap it when they can classify the error.
var ErrContextWindow = errors.New("context window exceeded")

// ErrStreamInterrupted marks a provider stream that ended before a complete
// response (disconnect, premature EOF, or a terminal read error). Partial
// content from such a stream must not be treated as a completed response.
var ErrStreamInterrupted = errors.New("provider stream interrupted")

// ErrInvalidStream marks a provider stream with malformed or contradictory
// protocol data. The stream cannot be recovered by continuing to read it.
var ErrInvalidStream = errors.New("invalid provider stream")

// IsTruncatedFinish reports whether a finish reason means the model hit its
// output token limit. Tool calls from such a response may have truncated
// arguments and must not be executed.
func IsTruncatedFinish(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

// IsContextWindowError reports whether err looks like a context-window
// overflow. It checks the sentinel first, then common provider error text.
func IsContextWindowError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextWindow) {
		return true
	}

	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context_length_exceeded",
		"context length",
		"context window",
		"context size",
		"maximum context",
		"max context",
		"maximum number of tokens",
		"too many tokens",
		"token limit",
		"prompt is too long",
		"input is too long",
		"reduce the length",
		"requested tokens exceed",
		"exceeds the model",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// Capabilities describes optional model features. Zero values mean the
// capability was not declared; providers must not assume it is supported.
type Capabilities struct {
	ContextWindow      int      `json:"context_window,omitempty"`
	ToolCalling        bool     `json:"tool_calling,omitempty"`
	Reasoning          bool     `json:"reasoning,omitempty"`
	StructuredOutput   bool     `json:"structured_output,omitempty"`
	Vision             bool     `json:"vision,omitempty"`
	SamplingParameters []string `json:"sampling_parameters,omitempty"`
}

// Message is one conversation message.
type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// Content is a text content block used by user messages.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Tool is a tool definition sent to the provider.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function describes a callable tool.
type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

// Parameters is a JSON Schema-style parameter description.
type Parameters struct {
	Type       string              `json:"type"`
	Required   []string            `json:"required,omitempty"`
	Properties map[string]Property `json:"properties,omitempty"`
}

// Property describes one parameter.
type Property struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Items       *Parameters `json:"items,omitempty"`
}

// ToolCall is a provider tool call, complete or a streaming delta.
type ToolCall struct {
	// Index identifies a streaming tool call. It is nil in assembled
	// responses.
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is a tool name and its JSON-encoded arguments.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// Usage reports token accounting for one provider call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Request is a provider-independent completion request.
type Request struct {
	Model            string    `json:"model,omitempty"`
	Messages         []Message `json:"messages"`
	Tools            []Tool    `json:"tools,omitempty"`
	MaxTokens        int       `json:"max_tokens,omitempty"`
	Temperature      float32   `json:"temperature,omitempty"`
	TopP             float32   `json:"top_p,omitempty"`
	TopK             int       `json:"top_k,omitempty"`
	MinP             float32   `json:"min_p,omitempty"`
	RepeatPenalty    float32   `json:"repeat_penalty,omitempty"`
	PresencePenalty  float32   `json:"presence_penalty,omitempty"`
	FrequencyPenalty float32   `json:"frequency_penalty,omitempty"`
}

// Delta is one streaming increment from a provider.
type Delta struct {
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// Response is the assembled result of one provider call.
type Response struct {
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	FinishReason     string     `json:"finish_reason,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	Usage            Usage      `json:"usage"`
}

// StreamEvent is one item in a provider stream. Implementations send zero or
// more DeltaEvents followed by exactly one DoneEvent or ErrorEvent.
type StreamEvent interface {
	streamEvent()
}

// DeltaEvent carries one incremental assistant, reasoning, or tool-call update.
type DeltaEvent struct {
	Delta Delta
}

func (DeltaEvent) streamEvent() {}

// DoneEvent is the terminal successful event and contains the assembled
// response.
type DoneEvent struct {
	Response Response
}

func (DoneEvent) streamEvent() {}

// ErrorEvent is the terminal error event for failures that occur after a
// stream has started.
type ErrorEvent struct {
	Err error
}

func (ErrorEvent) streamEvent() {}

// Provider is the stable LLM backend interface. Stream starts a request and
// returns a channel of events. Setup errors are returned directly; errors after
// the stream starts are sent as an ErrorEvent. Implementations must close the
// channel after the terminal event and must honour context cancellation for
// the request, retries, and streaming.
type Provider interface {
	Stream(ctx context.Context, request Request) (<-chan StreamEvent, error)
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(ctx context.Context, request Request) (<-chan StreamEvent, error)

// Stream implements Provider.
func (f ProviderFunc) Stream(ctx context.Context, request Request) (<-chan StreamEvent, error) {
	return f(ctx, request)
}

// ResponseFunc adapts a non-streaming completion function to Provider. It is
// useful for simple providers and tests.
type ResponseFunc func(ctx context.Context, request Request) (Response, error)

// Stream implements Provider.
func (f ResponseFunc) Stream(ctx context.Context, request Request) (<-chan StreamEvent, error) {
	response, err := f(ctx, request)
	if err != nil {
		return nil, err
	}
	events := make(chan StreamEvent, 1)
	events <- DoneEvent{Response: response}
	close(events)
	return events, nil
}

// Collect consumes a provider stream and returns its final response. It is a
// convenience for non-streaming callers and tests.
func Collect(ctx context.Context, p Provider, request Request) (Response, error) {
	if p == nil {
		return Response{}, fmt.Errorf("provider is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events, err := p.Stream(ctx, request)
	if err != nil {
		return Response{}, err
	}
	if events == nil {
		return Response{}, fmt.Errorf("%w: provider returned a nil stream", ErrInvalidStream)
	}

	for {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case event, ok := <-events:
			if !ok {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Response{}, ctxErr
				}
				return Response{}, fmt.Errorf("%w: stream closed without a terminal event", ErrStreamInterrupted)
			}
			switch value := event.(type) {
			case DeltaEvent:
				continue
			case DoneEvent:
				return value.Response, nil
			case ErrorEvent:
				if value.Err == nil {
					return Response{}, fmt.Errorf("%w: provider stream failed", ErrInvalidStream)
				}
				return Response{}, value.Err
			default:
				return Response{}, fmt.Errorf("%w: unknown stream event %T", ErrInvalidStream, event)
			}
		}
	}
}
