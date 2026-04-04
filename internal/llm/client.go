package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/m4ns0ur/iris/internal/logger"
)

var Logger = logger.Logger

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMOptions struct {
	Temperature       float64
	TopP              float64
	TopK              int
	MinP              float64
	PresencePenalty   float64
	RepetitionPenalty float64
}

type llmResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
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

func (c *LLMClient) Generate(systemPrompt string, userPrompt string, opts *LLMOptions) (string, error) {
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

	payload := map[string]any{
		"model": c.Model,
		"messages": []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		"max_tokens":       32768,
		"temperature":      options.Temperature,
		"top_p":            options.TopP,
		"presence_penalty": options.PresencePenalty,
		"response_format": map[string]any{
			"type": "json_object",
		},
		"extra_body": map[string]any{
			"top_k":              options.TopK,
			"min_p":              options.MinP,
			"repetition_penalty": options.RepetitionPenalty,
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		},
	}

	doRequest := func() (*http.Response, error) {
		Logger.LogLLMPayload(payload)

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

	// first attempt
	res, err := doRequest()
	if err != nil {
		Logger.Warn("LLM request failed, retrying once...", "error", err)

		res, err = doRequest()
		if err != nil {
			return "", err
		}
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	text := string(raw)
	Logger.LogLLMResponse(text)

	var parsed llmResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}

	if len(parsed.Choices) == 0 {
		return "", nil
	}

	return parsed.Choices[0].Message.Content, nil
}
