package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/config"
	"github.com/m4ns0ur/iris/internal/logger"
)

var Logger = logger.Logger

type Payload struct {
	Model            string        `json:"model"`
	Stream           bool          `json:"stream"`
	StreamOptions    StreamOptions `json:"stream_options,omitempty"`
	Store            bool          `json:"store,omitempty"`
	Messages         []ChatMessage `json:"messages"`
	Tools            []Tool        `json:"tools,omitempty"`
	MaxTokens        int           `json:"max_tokens"`
	Temperature      float32       `json:"temperature"`
	TopP             float32       `json:"top_p,omitempty"`
	TopK             int           `json:"top_k,omitempty"`
	MinP             float32       `json:"min_p,omitempty"`
	RepeatPenalty    float32       `json:"repeat_penalty,omitempty"`
	PresencePenalty  float32       `json:"presence_penalty,omitempty"`
	FrequencyPenalty float32       `json:"frequency_penalty,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ChatMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

type Parameters struct {
	Type       string              `json:"type"`
	Required   []string            `json:"required"`
	Properties map[string]Property `json:"properties"`
}

type Property struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Items       *Parameters `json:"items,omitempty"`
}

type LLMResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
	Timings Timings  `json:"timings"`
}

type Choice struct {
	Message Message `json:"message"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Timings struct {
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

type Chunk struct {
	Choices []ChunkChoice `json:"choices"`
}

type ChunkChoice struct {
	FinishReason string `json:"finish_reason,omitempty"`
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
}

type Delta struct {
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	// Index is present in streaming chunks to identify which tool call
	// this delta belongs to. It is omitted in non-streaming responses.
	Index    *int         `json:"index,omitempty"`
	Id       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type LLMClient struct {
	URL            string
	MaxRetries     int
	RequestTimeout time.Duration
	Model          string
	HTTP           *http.Client
}

// DefaultRequestTimeout bounds one complete LLM request, including retry
// attempts and stream reading. It can be overridden with
// [llm].request_timeout_seconds.
const DefaultRequestTimeout = 30 * time.Minute

func NewClient() *LLMClient {
	cfg := config.GetConfig()

	maxRetries := cfg.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	requestTimeout := cfg.LLM.RequestTimeout()
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}

	return &LLMClient{
		URL:            cfg.BaseURL + "/chat/completions",
		MaxRetries:     maxRetries,
		RequestTimeout: requestTimeout,
		Model:          cfg.DefaultModel,
		HTTP: &http.Client{
			Timeout: 0, // controlled via context per request
		},
	}
}

// HTTPStatusError describes a non-2xx response from the LLM server.
type HTTPStatusError struct {
	Status int
	Body   string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// Generate requests a completion. A context is required so callers can
// cancel the request, its retries, and an in-progress stream.
func (c *LLMClient) Generate(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
	onChunk func(chunkType string, content string),
	onError func(error),
) (Chunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	Logger.Debug("Generating response from LLM", "messageCount", len(messages), "toolCount", len(tools))

	if onChunk != nil || onError != nil {
		return c.GenerateStream(ctx, messages, tools, onChunk, onError)
	}
	return Chunk{}, fmt.Errorf("synchronous generation is not supported, onChunk or onError callback must be provided")
}

// GenerateStream streams a completion. Cancellation of ctx aborts the HTTP
// request, any retry backoff, and stream reading.
func (c *LLMClient) GenerateStream(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
	onChunk func(chunkType string, content string),
	onError func(error),
) (Chunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}

	// Guard against nil callbacks to avoid panics.
	if onChunk == nil {
		onChunk = func(string, string) {}
	}
	if onError == nil {
		onError = func(error) {}
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if c.RequestTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.RequestTimeout)
		defer cancel()
	}

	payload := createPayload(c.Model, messages, tools)
	payload.Stream = true
	payload.StreamOptions = StreamOptions{
		IncludeUsage: true,
	}

	LogLLMPayload(payload)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	doRequest := func() (*http.Response, error) {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode LLM request: %w", err)
		}

		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.URL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		res, err := httpClient.Do(req)
		if err != nil {
			if res != nil && res.Body != nil {
				_ = res.Body.Close()
			}
			return nil, err
		}

		if res.StatusCode != http.StatusOK {
			defer res.Body.Close()

			body, readErr := io.ReadAll(io.LimitReader(res.Body, 64*1024))
			if readErr != nil {
				return nil, fmt.Errorf("HTTP %d (failed to read error body: %w)", res.StatusCode, readErr)
			}
			return nil, &HTTPStatusError{Status: res.StatusCode, Body: string(body)}
		}

		return res, nil
	}

	attempts := c.MaxRetries
	if attempts < 1 {
		attempts = 1
	}

	var (
		res        *http.Response
		err        error
		retryDelay = time.Second
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		if callCtx.Err() != nil {
			return Chunk{}, c.requestContextError(ctx, callCtx)
		}

		if attempt > 1 {
			Logger.Warn(
				"LLM streaming request failed, retrying...",
				"attempt", attempt,
				"maxAttempts", attempts,
			)
			if err != nil {
				onError(fmt.Errorf("LLM streaming request failed on attempt %d: %w", attempt-1, err))
			}

			timer := time.NewTimer(retryDelay)
			select {
			case <-callCtx.Done():
				timer.Stop()
				return Chunk{}, c.requestContextError(ctx, callCtx)
			case <-timer.C:
			}
			retryDelay *= 2
		}

		res, err = doRequest()
		if err == nil && res != nil {
			break
		}

		if callCtx.Err() != nil {
			return Chunk{}, c.requestContextError(ctx, callCtx)
		}

		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) &&
			statusErr.Status >= 400 &&
			statusErr.Status < 500 &&
			statusErr.Status != http.StatusTooManyRequests {
			// Client errors are not transient; retrying cannot help.
			Logger.Error("LLM request rejected", "status", statusErr.Status)
			return Chunk{}, err
		}

		if attempt < attempts {
			Logger.Warn(
				"LLM streaming request failed",
				"attempt", attempt,
				"error", err,
			)
		} else {
			Logger.Error("LLM streaming request failed after all attempts", "error", err)
		}
	}

	if res == nil || err != nil {
		if err == nil {
			err = errors.New("LLM server returned no response")
		}
		if callCtx.Err() != nil {
			return Chunk{}, c.requestContextError(ctx, callCtx)
		}
		return Chunk{}, fmt.Errorf("failed to connect to LLM server after %d attempts: %w", attempts, err)
	}
	defer res.Body.Close()

	reader := bufio.NewReader(res.Body)

	var (
		fullResponse  strings.Builder
		fullReasoning strings.Builder

		// toolCallAccum maps streaming index → accumulated ToolCall.
		// The first chunk for a given tool call carries id/type/name;
		// subsequent chunks only carry argument fragments.
		toolCallAccum = make(map[int]*ToolCall)
		toolCallOrder []int // preserves insertion order
	)

	finalChunk := Chunk{
		Choices: []ChunkChoice{
			{
				Delta: Delta{},
			},
		},
	}

	for {
		if callCtx.Err() != nil {
			return finalChunk, c.requestContextError(ctx, callCtx)
		}

		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if callCtx.Err() != nil {
				return finalChunk, c.requestContextError(ctx, callCtx)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			// Return whatever we have accumulated alongside the error so the
			// caller can decide whether to use the partial response.
			return finalChunk, fmt.Errorf("error reading stream: %w", readErr)
		}

		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))
		Logger.Debug("Received stream chunk", "chunk", string(data))

		if string(data) == "[DONE]" {
			break
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			Logger.Error(
				"Failed to parse chunk as JSON",
				"error", err,
				"chunk", string(data),
			)
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if callCtx.Err() != nil {
			return finalChunk, c.requestContextError(ctx, callCtx)
		}

		choice := chunk.Choices[0]
		finalChunk.Choices[0].FinishReason = choice.FinishReason

		if rc := choice.Delta.ReasoningContent; rc != "" {
			fullReasoning.WriteString(rc)
			onChunk("reason", rc)
		}

		// Accumulate tool call deltas keyed by their streaming index.
		for _, tc := range choice.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}

			acc, exists := toolCallAccum[idx]
			if !exists {
				// First chunk for this tool call: capture id/type/name.
				acc = &ToolCall{
					Id:   tc.Id,
					Type: tc.Type,
					Function: FunctionCall{
						Name: tc.Function.Name,
					},
				}
				toolCallAccum[idx] = acc
				toolCallOrder = append(toolCallOrder, idx)
			}

			acc.Function.Arguments += tc.Function.Arguments

			// Emit raw argument fragments for callers that want to stream
			// tool call progress (e.g. progress indicators). Callers must
			// treat these as partial/incomplete JSON.
			if tc.Function.Arguments != "" {
				b, err := json.Marshal(tc.Function)
				if err == nil {
					onChunk("tool", string(b))
				}
			}
		}

		if content := choice.Delta.Content; content != "" {
			fullResponse.WriteString(content)
			onChunk("response", content)
		}
	}

	if callCtx.Err() != nil {
		return finalChunk, c.requestContextError(ctx, callCtx)
	}

	finalChunk.Choices[0].Delta.Content = fullResponse.String()
	finalChunk.Choices[0].Delta.ReasoningContent = fullReasoning.String()

	if len(toolCallAccum) > 0 {
		assembled := make([]ToolCall, 0, len(toolCallOrder))
		for _, idx := range toolCallOrder {
			assembled = append(assembled, *toolCallAccum[idx])
		}
		finalChunk.Choices[0].Delta.ToolCalls = assembled
	}

	return finalChunk, nil
}

// requestContextError turns a cancelled call context into the most useful
// error: the parent's cancellation takes priority, then the request deadline.
func (c *LLMClient) requestContextError(parent, callCtx context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}

	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("LLM request timed out after %s: %w", c.RequestTimeout, context.DeadlineExceeded)
	}

	if err := callCtx.Err(); err != nil {
		return err
	}

	return context.Canceled
}

func createPayload(model string, messages []ChatMessage, tools []Tool) Payload {
	payload := Payload{
		Model:       model,
		Store:       false,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   32768,
		Temperature: 0.6,
	}

	defaultModel := config.DefaultModel()
	if defaultModel.Temperature != 0 {
		payload.Temperature = defaultModel.Temperature
	}
	if defaultModel.TopP != 0 {
		payload.TopP = defaultModel.TopP
	}
	if defaultModel.TopK != 0 {
		payload.TopK = defaultModel.TopK
	}
	if defaultModel.MinP != 0 {
		payload.MinP = defaultModel.MinP
	}
	if defaultModel.RepeatPenalty != 0 {
		payload.RepeatPenalty = defaultModel.RepeatPenalty
	}
	if defaultModel.PresencePenalty != 0 {
		payload.PresencePenalty = defaultModel.PresencePenalty
	}
	if defaultModel.FrequencyPenalty != 0 {
		payload.FrequencyPenalty = defaultModel.FrequencyPenalty
	}

	return payload
}
