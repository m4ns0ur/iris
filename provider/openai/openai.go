// Package openai provides a ready-to-use provider for OpenAI-compatible chat
// completion servers. It is the same transport Iris uses internally and lets
// Go applications embed Iris without implementing provider.Provider.
package openai

import (
	"net/http"
	"strings"
	"time"

	"github.com/m4ns0ur/iris/internal/llm"
	"github.com/m4ns0ur/iris/provider"
)

// DefaultBaseURL is used when Options.BaseURL is empty.
const DefaultBaseURL = "http://127.0.0.1:8080/v1"

// Options configures an OpenAI-compatible provider.
type Options struct {
	// BaseURL is the API root, for example http://localhost:8080/v1.
	BaseURL string

	// APIKey is sent as a Bearer token when non-empty.
	APIKey string

	// Model is the default model name sent with requests.
	Model string

	// MaxRetries is the number of HTTP attempts. Zero uses 3.
	MaxRetries int

	// RequestTimeout bounds one complete request. Zero uses 30 minutes.
	RequestTimeout time.Duration

	// HTTPClient optionally overrides the HTTP client.
	HTTPClient *http.Client
}

// New creates an OpenAI-compatible provider.
func New(options Options) provider.Provider {
	baseURL := strings.TrimSpace(options.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	maxRetries := options.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = llm.DefaultRequestTimeout
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 0}
	}

	return &llm.LLMClient{
		URL:            baseURL + "/chat/completions",
		APIKey:         options.APIKey,
		MaxRetries:     maxRetries,
		RequestTimeout: timeout,
		Model:          options.Model,
		HTTP:           httpClient,
	}
}
