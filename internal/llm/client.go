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
	"github.com/m4ns0ur/iris/provider"
)

var Logger = logger.Logger

// Public provider types are re-exported here so existing Iris code can keep
// using the llm package while the stable API lives in provider.
type (
	ChatMessage  = provider.Message
	Content      = provider.Content
	Tool         = provider.Tool
	Function     = provider.Function
	Parameters   = provider.Parameters
	Property     = provider.Property
	ToolCall     = provider.ToolCall
	FunctionCall = provider.FunctionCall
	Usage        = provider.Usage
)

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

// LLMResponse and its children are retained for debug logging of
// non-streaming responses.
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

type Timings struct {
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

type Chunk struct {
	Choices []ChunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
	Error   *ChunkError   `json:"error,omitempty"`
}

// ChunkError is an error reported inside a streamed response body. Some
// OpenAI-compatible servers return HTTP 200 and then send an error object as a
// data chunk.
type ChunkError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Param   string `json:"param,omitempty"`
	Code    any    `json:"code,omitempty"`
}

func (e *ChunkError) Error() string {
	if e == nil {
		return "provider stream error"
	}
	if e.Message != "" {
		return e.Message
	}
	return "provider stream error"
}

type ChunkChoice struct {
	FinishReason string         `json:"finish_reason,omitempty"`
	Index        int            `json:"index"`
	Delta        provider.Delta `json:"delta"`
}

var _ provider.Provider = (*LLMClient)(nil)

type LLMClient struct {
	URL            string
	APIKey         string
	MaxRetries     int
	RequestTimeout time.Duration
	Model          string
	HTTP           *http.Client

	// RetryBaseDelay is the first retry backoff. Zero uses one second. Tests
	// may override it to avoid waiting.
	RetryBaseDelay time.Duration
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

	// RetryAfter is the server-provided retry delay, if any.
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// Stream implements provider.Provider. Connection/setup errors are returned
// directly; errors after the stream starts are sent as ErrorEvent.
func (c *LLMClient) Stream(
	ctx context.Context,
	request provider.Request,
) (<-chan provider.StreamEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if c.RequestTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.RequestTimeout)
	}
	cleanup := func() {
		if cancel != nil {
			cancel()
		}
	}

	payload := createPayload(c.Model, request)
	payload.Stream = true
	payload.StreamOptions = StreamOptions{IncludeUsage: true}

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
		if c.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}

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
			return nil, &HTTPStatusError{
				Status:     res.StatusCode,
				Body:       string(body),
				RetryAfter: parseRetryAfter(res.Header.Get("Retry-After"), time.Now()),
			}
		}

		return res, nil
	}

	attempts := c.MaxRetries
	if attempts < 1 {
		attempts = 1
	}

	baseDelay := c.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}

	var (
		res        *http.Response
		err        error
		retryDelay = baseDelay
		retryAfter time.Duration
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		if callCtx.Err() != nil {
			cleanup()
			return nil, c.requestContextError(ctx, callCtx)
		}

		if attempt > 1 {
			wait := retryDelay
			if retryAfter > wait {
				wait = retryAfter
			}
			if wait > maxRetryDelay {
				wait = maxRetryDelay
			}

			Logger.Warn(
				"LLM streaming request failed, retrying...",
				"attempt", attempt,
				"maxAttempts", attempts,
				"delay", wait,
			)

			timer := time.NewTimer(wait)
			select {
			case <-callCtx.Done():
				timer.Stop()
				cleanup()
				return nil, c.requestContextError(ctx, callCtx)
			case <-timer.C:
			}
			retryDelay *= 2
			if retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
		}

		retryAfter = 0
		res, err = doRequest()
		if err == nil && res != nil {
			// Retries stop at the response boundary. Once a stream has started,
			// a disconnect terminates the request cleanly instead of silently
			// repeating it: tools are only executed after a complete response.
			break
		}

		if callCtx.Err() != nil {
			cleanup()
			return nil, c.requestContextError(ctx, callCtx)
		}

		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) {
			retryAfter = statusErr.RetryAfter
			// Context overflow is not a transient transport failure. Some
			// servers report it with a 5xx status, so classify it before the
			// retryable status check to let the runtime compact instead.
			if statusErr.Status == http.StatusRequestEntityTooLarge ||
				provider.IsContextWindowError(statusErr) {
				Logger.Error("LLM request rejected", "status", statusErr.Status)
				cleanup()
				return nil, fmt.Errorf("%w: %w", provider.ErrContextWindow, err)
			}
			if !retryableStatus(statusErr.Status) {
				// Client errors are deterministic; retrying cannot help.
				Logger.Error("LLM request rejected", "status", statusErr.Status)
				cleanup()
				return nil, err
			}
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
			cleanup()
			return nil, c.requestContextError(ctx, callCtx)
		}
		cleanup()
		return nil, fmt.Errorf("LLM request failed after %d attempts: %w", attempts, err)
	}

	events := make(chan provider.StreamEvent)
	go func() {
		defer close(events)
		defer res.Body.Close()
		if cancel != nil {
			defer cancel()
		}
		c.readStream(callCtx, ctx, res.Body, events)
	}()

	return events, nil
}

func (c *LLMClient) readStream(
	callCtx context.Context,
	parent context.Context,
	body io.Reader,
	events chan<- provider.StreamEvent,
) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var (
		fullResponse  strings.Builder
		fullReasoning strings.Builder
		toolCalls     = newToolCallAccumulator()
		usage         provider.Usage
		finishReason  string
		sawDone       bool
	)

	assembledResponse := func() provider.Response {
		return provider.Response{
			Content:          fullResponse.String(),
			ReasoningContent: fullReasoning.String(),
			FinishReason:     finishReason,
			ToolCalls:        toolCalls.callsResult(),
			Usage:            usage,
		}
	}

	send := func(event provider.StreamEvent) bool {
		// Prefer delivering the event if a consumer is ready, even when the
		// request context has just been cancelled or timed out. This keeps
		// terminal error events observable.
		select {
		case events <- event:
			return true
		default:
		}
		select {
		case events <- event:
			return true
		case <-callCtx.Done():
			return false
		}
	}

	fail := func(err error) {
		_ = send(provider.ErrorEvent{Err: err})
	}

	for scanner.Scan() {
		if callCtx.Err() != nil {
			fail(c.requestContextError(parent, callCtx))
			return
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		Logger.Debug("Received stream chunk", "chunk", string(data))

		if bytes.Equal(data, []byte("[DONE]")) {
			sawDone = true
			break
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			Logger.Error(
				"Failed to parse chunk as JSON",
				"error", err,
				"chunk", string(data),
			)
			fail(fmt.Errorf("%w: decode chunk: %v", provider.ErrInvalidStream, err))
			return
		}

		if chunk.Error != nil {
			fail(fmt.Errorf("provider stream error: %w", chunk.Error))
			return
		}

		if chunk.Usage != nil {
			usage = *chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if callCtx.Err() != nil {
			fail(c.requestContextError(parent, callCtx))
			return
		}

		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		delta := choice.Delta

		if rc := delta.ReasoningContent; rc != "" {
			fullReasoning.WriteString(rc)
		}

		for _, tc := range delta.ToolCalls {
			toolCalls.add(tc)
		}

		if content := delta.Content; content != "" {
			fullResponse.WriteString(content)
		}

		if delta.ReasoningContent != "" || delta.Content != "" || len(delta.ToolCalls) > 0 {
			if !send(provider.DeltaEvent{Delta: delta}) {
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if callCtx.Err() != nil {
			fail(c.requestContextError(parent, callCtx))
			return
		}
		fail(fmt.Errorf("%w: %v", provider.ErrStreamInterrupted, err))
		return
	}

	if callCtx.Err() != nil {
		fail(c.requestContextError(parent, callCtx))
		return
	}

	// [DONE] is the canonical terminator. Some compatible servers close the
	// body after a chunk with finish_reason but omit [DONE]; accept that.
	// Anything else is an interrupted stream and must not look successful.
	if !sawDone && finishReason == "" {
		fail(fmt.Errorf("%w: stream ended before the response completed", provider.ErrStreamInterrupted))
		return
	}

	_ = send(provider.DoneEvent{Response: assembledResponse()})
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

func createPayload(model string, request provider.Request) Payload {
	payload := Payload{
		Model:            request.Model,
		Store:            false,
		Messages:         request.Messages,
		Tools:            request.Tools,
		MaxTokens:        request.MaxTokens,
		Temperature:      request.Temperature,
		TopP:             request.TopP,
		TopK:             request.TopK,
		MinP:             request.MinP,
		RepeatPenalty:    request.RepeatPenalty,
		PresencePenalty:  request.PresencePenalty,
		FrequencyPenalty: request.FrequencyPenalty,
	}

	if payload.Model == "" {
		payload.Model = model
	}
	if payload.Model == "" {
		payload.Model = config.GetConfig().DefaultModel
	}
	if payload.MaxTokens == 0 {
		payload.MaxTokens = 32768
	}
	if payload.Temperature == 0 {
		payload.Temperature = 0.6
	}

	defaultModel := config.DefaultModel()
	if request.Temperature == 0 && defaultModel.Temperature != 0 {
		payload.Temperature = defaultModel.Temperature
	}
	if request.TopP == 0 && defaultModel.TopP != 0 {
		payload.TopP = defaultModel.TopP
	}
	if request.TopK == 0 && defaultModel.TopK != 0 {
		payload.TopK = defaultModel.TopK
	}
	if request.MinP == 0 && defaultModel.MinP != 0 {
		payload.MinP = defaultModel.MinP
	}
	if request.RepeatPenalty == 0 && defaultModel.RepeatPenalty != 0 {
		payload.RepeatPenalty = defaultModel.RepeatPenalty
	}
	if request.PresencePenalty == 0 && defaultModel.PresencePenalty != 0 {
		payload.PresencePenalty = defaultModel.PresencePenalty
	}
	if request.FrequencyPenalty == 0 && defaultModel.FrequencyPenalty != 0 {
		payload.FrequencyPenalty = defaultModel.FrequencyPenalty
	}

	return payload
}
