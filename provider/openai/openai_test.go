package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m4ns0ur/iris/provider"
)

func TestNewSendsAuthAndUsesBaseURL(t *testing.T) {
	var (
		gotPath  string
		gotAuth  string
		gotModel string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel = payload.Model

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := New(Options{
		BaseURL:        server.URL + "/v1/",
		APIKey:         "secret",
		Model:          "default-model",
		MaxRetries:     1,
		RequestTimeout: 5 * time.Second,
		HTTPClient:     server.Client(),
	})

	response, err := provider.Collect(context.Background(), p, provider.Request{Model: "request-model"})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("content = %q", response.Content)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotModel != "request-model" {
		t.Fatalf("model = %q", gotModel)
	}
}
