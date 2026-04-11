package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/m4ns0ur/iris/internal/logger"
)

const MaxRetries = 3

var Logger = logger.Logger

type Payload struct {
	Model           string         `json:"model"`
	Stream          bool           `json:"stream"`
	Messages        []ChatMessage  `json:"messages"`
	MaxTokens       int            `json:"max_tokens"`
	Temperature     float64        `json:"temperature"`
	TopP            float64        `json:"top_p"`
	PresencePenalty float64        `json:"presence_penalty"`
	ResponseFormat  ResponseFormat `json:"response_format"`
	ExtraBody       ExtraBody      `json:"extra_body"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	ResponseType string `json:"type"`
}

type ExtraBody struct {
	TopK               int                `json:"top_k"`
	MinP               float64            `json:"min_p"`
	RepetitionPenalty  float64            `json:"repetition_penalty"`
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
	Delta Delta `json:"delta"`
}

type Delta struct {
	Content string `json:"content"`
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
			Timeout: 0, // we control timeout via context
		},
	}
}

func (c *LLMClient) Generate(systemPrompt string, userPrompt string, opts *LLMOptions, onChunk func(string), onError func(error)) (string, error) {
	if onChunk != nil || onError != nil {
		err := c.GenerateStreamWithCallbacks(systemPrompt, userPrompt, opts, onChunk, onError)
		if err != nil && onError != nil {
			onError(err)
		}
		return "", err
	}
	return c.GenerateSync(systemPrompt, userPrompt, opts)
}

func (c *LLMClient) GenerateSync(systemPrompt string, userPrompt string, opts *LLMOptions) (string, error) {
	payload := createPayload(c.Model, systemPrompt, userPrompt, opts)

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
			return nil, fmt.Errorf("HTTP %d %s", res.StatusCode, res.Status)
		}

		return res, nil
	}

	var res *http.Response
	var err error

	retryDelay := 1 * time.Second

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			Logger.Warn("LLM request failed, retrying...", "attempt", attempt, "maxRetries", MaxRetries)
			time.Sleep(retryDelay)
		}

		res, err = doRequest()
		if err == nil {
			break
		}

		// Exponential backoff for retry delay
		retryDelay *= 2

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

	return llmResponse.Choices[0].Message.Content, nil
}

func (c *LLMClient) GenerateStream(systemPrompt string, userPrompt string, opts *LLMOptions, onChunk func(string)) error {
	payload := createPayload(c.Model, systemPrompt, userPrompt, opts)
	payload.Stream = true

	LogLLMPayload(payload)

	doRequest := func() (*http.Response, error) {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest("POST", c.URL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", res.StatusCode)
		}

		return res, nil
	}

	var res *http.Response
	var err error

	retryDelay := 1 * time.Second

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			Logger.Warn("LLM streaming request failed, retrying...", "attempt", attempt, "maxRetries", MaxRetries)
			time.Sleep(retryDelay)
		}

		res, err = doRequest()
		if err == nil && res != nil {
			break
		}

		// Exponential backoff for retry delay
		retryDelay *= 2

		if attempt < MaxRetries {
			Logger.Warn("LLM streaming request failed", "attempt", attempt, "error", err)
		} else {
			Logger.Error("LLM streaming request failed after all retries", "error", err)
		}
	}

	if res == nil || err != nil {
		return fmt.Errorf("failed to connect to LLM server after %d attempts: %w", MaxRetries, err)
	}

	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// OpenAI SSE lines start with "data: "
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))

		// Check for the [DONE] signal
		if string(data) == "[DONE]" {
			break
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue // Or handle error
		}

		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				onChunk(content)
			}
		}
	}

	return nil
}

func (c *LLMClient) GenerateStreamWithCallbacks(systemPrompt string, userPrompt string, opts *LLMOptions, onChunk func(string), onError func(error)) error {
	payload := createPayload(c.Model, systemPrompt, userPrompt, opts)
	payload.Stream = true

	LogLLMPayload(payload)

	doRequest := func() (*http.Response, error) {
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequest("POST", c.URL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", res.StatusCode)
		}

		return res, nil
	}

	var res *http.Response
	var err error

	retryDelay := 1 * time.Second

	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if attempt > 1 {
			Logger.Warn("LLM streaming request with callbacks failed, retrying...", "attempt", attempt, "maxRetries", MaxRetries)
			time.Sleep(retryDelay)
		}

		res, err = doRequest()
		if err == nil && res != nil {
			break
		}

		// Exponential backoff for retry delay
		retryDelay *= 2

		if attempt < MaxRetries {
			Logger.Warn("LLM streaming request with callbacks failed", "attempt", attempt, "error", err)
		} else {
			Logger.Error("LLM streaming request with callbacks failed after all retries", "error", err)
		}
	}

	if res == nil || err != nil {
		return fmt.Errorf("failed to connect to LLM server after %d attempts: %w", MaxRetries, err)
	}

	reader := bufio.NewReader(res.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// OpenAI SSE lines start with "data: "
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))

		// Check for the [DONE] signal
		if string(data) == "[DONE]" {
			break
		}

		var chunk Chunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue // Or handle error
		}

		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				onChunk(content)
			}
		}
	}

	return nil
}

func createPayload(model, systemPrompt, userPrompt string, opts *LLMOptions) Payload {
	options := LLMOptions{
		Temperature:       0.2,
		TopP:              0.95,
		TopK:              40,
		MinP:              0.05,
		PresencePenalty:   0.0,
		RepetitionPenalty: 1.1,
	}
	if opts != nil {
		if opts.Temperature != 0 {
			options.Temperature = opts.Temperature
		}
		if opts.TopP != 0 {
			options.TopP = opts.TopP
		}
		if opts.TopK != 0 {
			options.TopK = opts.TopK
		}
		if opts.MinP != 0 {
			options.MinP = opts.MinP
		}
		if opts.PresencePenalty != 0 {
			options.PresencePenalty = opts.PresencePenalty
		}
		if opts.RepetitionPenalty != 0 {
			options.RepetitionPenalty = opts.RepetitionPenalty
		}
	}

	payload := Payload{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:       32768,
		Temperature:     options.Temperature,
		TopP:            options.TopP,
		PresencePenalty: options.PresencePenalty,
		ResponseFormat: ResponseFormat{
			ResponseType: "json_object",
		},
		ExtraBody: ExtraBody{
			TopK:              options.TopK,
			MinP:              options.MinP,
			RepetitionPenalty: options.RepetitionPenalty,
			ChatTemplateKwargs: ChatTemplateKwargs{
				EnableThinking: false,
			},
		},
	}
	return payload
}
