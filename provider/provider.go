// Package provider defines the stable interface between the Iris agent
// runtime and LLM backends.
package provider

import "context"

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

// Provider is the stable LLM backend interface. Generate streams deltas to
// onDelta (which may be nil) and returns the assembled response. Implementations
// must honour context cancellation for the request, retries, and streaming.
type Provider interface {
	Generate(ctx context.Context, request Request, onDelta func(Delta)) (Response, error)
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(ctx context.Context, request Request, onDelta func(Delta)) (Response, error)

// Generate implements Provider.
func (f ProviderFunc) Generate(ctx context.Context, request Request, onDelta func(Delta)) (Response, error) {
	return f(ctx, request, onDelta)
}
