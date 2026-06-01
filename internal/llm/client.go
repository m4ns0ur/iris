package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/logger"
)

const MaxRetries = 3

var Logger = logger.Logger

type Payload struct {
	Model           string         `json:"model"`
	Stream          bool           `json:"stream"`
	Messages        []ChatMessage  `json:"messages"`
	Tools           []Tool         `json:"tools,omitempty"`
	MaxTokens       int            `json:"max_tokens"`
	Temperature     float64        `json:"temperature"`
	TopP            float64        `json:"top_p,omitempty"`
	PresencePenalty float64        `json:"presence_penalty,omitempty"`
	ResponseFormat  ResponseFormat `json:"response_format,omitempty"`
	ExtraBody       ExtraBody      `json:"extra_body"`
	CachePrompt     bool           `json:"cache_prompt"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

type ResponseFormat struct {
	ResponseType string `json:"type,omitempty"`
}

type ExtraBody struct {
	TopK               int                `json:"top_k,omitempty"`
	MinP               float64            `json:"min_p,omitempty"`
	RepetitionPenalty  float64            `json:"repetition_penalty,omitempty"`
	ChatTemplateKwargs ChatTemplateKwargs `json:"chat_template_kwargs"`
}

type ChatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
}

type LLMOptions struct {
	Temperature       float64
	TopP              float64
	TopK              int
	MinP              float64
	PresencePenalty   float64
	RepetitionPenalty float64
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
	Model string
	URL   string
	HTTP  *http.Client
}

func NewClient(model string) *LLMClient {
	if model == "" {
		model = "bartowski/Qwen_Qwen3.5-9B-GGUF:Q5_K_M"
	}

	return &LLMClient{
		Model: model,
		URL:   "http://localhost:8080/v1/chat/completions",
		HTTP: &http.Client{
			Timeout: 0, // controlled via context per-request
		},
	}
}

func (c *LLMClient) Generate(messages []ChatMessage, tools []Tool, opts *LLMOptions, onChunk func(chunkType string, content string), onError func(error)) (Chunk, error) {
	if onChunk != nil || onError != nil {
		return c.GenerateStream(messages, tools, opts, onChunk, onError)
	}
	return Chunk{}, fmt.Errorf("synchronous generation is not supported, onChunk or onError callback must be provided")
}

func (c *LLMClient) GenerateSync(messages []ChatMessage, tools []Tool, opts *LLMOptions) (string, error) {
	payload := createPayload(c.Model, messages, tools, opts)

	LogLLMPayload(payload)

	doRequest := func() (*http.Response, error) {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		res, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				Logger.Error("Request timed out")
			}
			return nil, err
		}

		if res.StatusCode < 200 || res.StatusCode >= 300 {
			res.Body.Close()
			return nil, fmt.Errorf("HTTP %d %s", res.StatusCode, res.Status)
		}

		return res, nil
	}

	var (
		res        *http.Response
		err        error
		retryDelay = time.Second
	)

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			Logger.Warn("LLM request failed, retrying...", "attempt", attempt, "maxRetries", MaxRetries)
			time.Sleep(retryDelay)
			retryDelay *= 2
		}

		res, err = doRequest()
		if err == nil {
			break
		}

		if attempt < MaxRetries {
			Logger.Warn("LLM request failed", "attempt", attempt, "error", err)
		} else {
			Logger.Error("LLM request failed after all retries", "error", err)
		}
	}

	if res == nil || err != nil {
		return "", fmt.Errorf("failed to connect to LLM server after %d attempts: %w", MaxRetries, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	var llmResponse LLMResponse
	if err := json.Unmarshal(raw, &llmResponse); err != nil {
		Logger.Error("Failed to parse LLM response as JSON", "error", err, "raw_response", string(raw))
		return "", err
	}

	LogLLMResponse(llmResponse)

	// TODO: handle reasoning content and tool calls in the response
	return llmResponse.Choices[0].Message.Content, nil
}

func (c *LLMClient) GenerateStream(messages []ChatMessage, tools []Tool, opts *LLMOptions, onChunk func(chunkType string, content string), onError func(error)) (Chunk, error) {
	// Guard against nil callbacks to avoid panics.
	if onChunk == nil {
		onChunk = func(string, string) {}
	}
	if onError == nil {
		onError = func(error) {}
	}

	payload := createPayload(c.Model, messages, tools, opts)
	payload.Stream = true

	LogLLMPayload(payload)

	doRequest := func() (*http.Response, error) {
		bodyBytes, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}

		// Mirror the timeout used in GenerateSync.
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
		// cancel is intentionally not deferred here. The context must stay
		// alive for the duration of the stream read below. The caller closes
		// res.Body which unblocks any in-flight reads, and the GC will clean
		// up the context. If you want explicit cancellation, thread a
		// context.Context through the method signature instead.
		_ = cancel // suppress "cancel not called" linter warning

		req, e := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(bodyBytes))
		if e != nil {
			cancel()
			return nil, e
		}
		req.Header.Set("Content-Type", "application/json")

		res, e := c.HTTP.Do(req)
		if e != nil {
			cancel()
			return nil, e
		}

		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			cancel()
			return nil, fmt.Errorf("HTTP %d", res.StatusCode)
		}

		return res, nil
	}

	var (
		res        *http.Response
		err        error
		retryDelay = time.Second
	)

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			Logger.Warn("LLM streaming request failed, retrying...", "attempt", attempt, "maxRetries", MaxRetries)
			if err != nil {
				onError(fmt.Errorf("LLM streaming request failed on attempt %d: %w", attempt-1, err))
			}
			time.Sleep(retryDelay)
			retryDelay *= 2
		}

		res, err = doRequest()
		if err == nil && res != nil {
			break
		}

		if attempt < MaxRetries {
			Logger.Warn("LLM streaming request failed", "attempt", attempt, "error", err)
		} else {
			Logger.Error("LLM streaming request failed after all retries", "error", err)
		}
	}

	if res == nil || err != nil {
		return Chunk{}, fmt.Errorf("failed to connect to LLM server after %d attempts: %w", MaxRetries, err)
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
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if readErr == io.EOF {
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
			Logger.Error("Failed to parse chunk as JSON", "error", err, "chunk", string(data))
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
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
				onChunk("tool", tc.Function.Arguments)
			}
		}

		if content := choice.Delta.Content; content != "" {
			fullResponse.WriteString(content)
			onChunk("response", content)
		}
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

func createPayload(model string, messages []ChatMessage, tools []Tool, opts *LLMOptions) Payload {
	payload := Payload{
		Model:       model,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   32768,
		Temperature: 0.80,
		ExtraBody: ExtraBody{
			ChatTemplateKwargs: ChatTemplateKwargs{
				EnableThinking: true,
			},
		},
		CachePrompt: true,
	}

	if opts != nil {
		if opts.Temperature != 0 {
			payload.Temperature = opts.Temperature
		}
		if opts.TopP != 0 {
			payload.TopP = opts.TopP
		}
		if opts.PresencePenalty != 0 {
			payload.PresencePenalty = opts.PresencePenalty
		}
		if opts.TopK != 0 {
			payload.ExtraBody.TopK = opts.TopK
		}
		if opts.MinP != 0 {
			payload.ExtraBody.MinP = opts.MinP
		}
		if opts.RepetitionPenalty != 0 {
			payload.ExtraBody.RepetitionPenalty = opts.RepetitionPenalty
		}
	}

	return payload
}
