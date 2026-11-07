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
}

type ChunkChoice struct {
	FinishReason string         `json:"finish_reason,omitempty"`
	Index        int            `json:"index"`
	Delta        provider.Delta `json:"delta"`
}

var _ provider.Provider = (*LLMClient)(nil)

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

// Generate implements provider.Provider. Cancellation of ctx aborts the HTTP
// request, any retry backoff, and stream reading.
func (c *LLMClient) Generate(
	ctx context.Context,
	request provider.Request,
	onDelta func(provider.Delta),
) (provider.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	if onDelta == nil {
		onDelta = func(provider.Delta) {}
	}

	callCtx := ctx
	var cancel context.CancelFunc
	if c.RequestTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.RequestTimeout)
		defer cancel()
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
			return provider.Response{}, c.requestContextError(ctx, callCtx)
		}

		if attempt > 1 {
			Logger.Warn(
				"LLM streaming request failed, retrying...",
				"attempt", attempt,
				"maxAttempts", attempts,
			)

			timer := time.NewTimer(retryDelay)
			select {
			case <-callCtx.Done():
				timer.Stop()
				return provider.Response{}, c.requestContextError(ctx, callCtx)
			case <-timer.C:
			}
			retryDelay *= 2
		}

		res, err = doRequest()
		if err == nil && res != nil {
			break
		}

		if callCtx.Err() != nil {
			return provider.Response{}, c.requestContextError(ctx, callCtx)
		}

		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) &&
			statusErr.Status >= 400 &&
			statusErr.Status < 500 &&
			statusErr.Status != http.StatusTooManyRequests {
			// Client errors are not transient; retrying cannot help.
			Logger.Error("LLM request rejected", "status", statusErr.Status)
			if statusErr.Status == http.StatusRequestEntityTooLarge ||
				provider.IsContextWindowError(statusErr) {
				return provider.Response{}, fmt.Errorf("%w: %w", provider.ErrContextWindow, err)
			}
			return provider.Response{}, err
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
			return provider.Response{}, c.requestContextError(ctx, callCtx)
		}
		return provider.Response{}, fmt.Errorf("failed to connect to LLM server after %d attempts: %w", attempts, err)
	}
	defer res.Body.Close()

	reader := bufio.NewReader(res.Body)

	var (
		fullResponse  strings.Builder
		fullReasoning strings.Builder

		// toolCallAccum maps streaming index → accumulated ToolCall.
		toolCallAccum = make(map[int]*provider.ToolCall)
		toolCallOrder []int
		usage         provider.Usage
		finishReason  string
	)

	assembledResponse := func() provider.Response {
		assembled := make([]provider.ToolCall, 0, len(toolCallOrder))
		for _, idx := range toolCallOrder {
			assembled = append(assembled, *toolCallAccum[idx])
		}

		return provider.Response{
			Content:          fullResponse.String(),
			ReasoningContent: fullReasoning.String(),
			FinishReason:     finishReason,
			ToolCalls:        assembled,
			Usage:            usage,
		}
	}

	for {
		if callCtx.Err() != nil {
			return assembledResponse(), c.requestContextError(ctx, callCtx)
		}

		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if callCtx.Err() != nil {
				return assembledResponse(), c.requestContextError(ctx, callCtx)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			return assembledResponse(), fmt.Errorf("error reading stream: %w", readErr)
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

		if chunk.Usage != nil {
			usage = *chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		if callCtx.Err() != nil {
			return assembledResponse(), c.requestContextError(ctx, callCtx)
		}

		choice := chunk.Choices[0]
		finishReason = choice.FinishReason
		delta := choice.Delta

		if rc := delta.ReasoningContent; rc != "" {
			fullReasoning.WriteString(rc)
		}

		for _, tc := range delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}

			acc, exists := toolCallAccum[idx]
			if !exists {
				acc = &provider.ToolCall{}
				toolCallAccum[idx] = acc
				toolCallOrder = append(toolCallOrder, idx)
			}
			if tc.ID != "" {
				acc.ID = tc.ID
			}
			if tc.Type != "" {
				acc.Type = tc.Type
			}
			if tc.Function.Name != "" {
				acc.Function.Name = tc.Function.Name
			}
			acc.Function.Arguments += tc.Function.Arguments
		}

		if content := delta.Content; content != "" {
			fullResponse.WriteString(content)
		}

		if delta.ReasoningContent != "" || delta.Content != "" || len(delta.ToolCalls) > 0 {
			onDelta(delta)
		}
	}

	if callCtx.Err() != nil {
		return assembledResponse(), c.requestContextError(ctx, callCtx)
	}

	return assembledResponse(), nil
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
