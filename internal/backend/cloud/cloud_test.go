package cloud_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
)

// T1.2.3
func TestInterfaceCompliance(t *testing.T) {
	var _ backend.InferenceBackend = (*cloud.OpenAIBackend)(nil)
	var _ backend.InferenceBackend = (*cloud.AnthropicBackend)(nil)
}

// T1.5.1 — OpenAI forward + stream
func TestOpenAI_Complete_Stream(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	b := cloud.NewOpenAI(srv.URL, "sk-test")
	ch, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hello"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for c := range ch {
		got += c.Content
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("auth=%q", auth)
	}
	if got != "hi" {
		t.Fatalf("content=%q", got)
	}
}

// T1.5.2 — Anthropic headers + format translation
func TestAnthropic_Complete_HeadersAndTranslate(t *testing.T) {
	var apiKey, version string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"},\"message\":{\"id\":\"m1\",\"model\":\"claude\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	b := cloud.NewAnthropic(srv.URL, "ant-key")
	ch, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model: "claude-sonnet",
		Messages: []backend.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for c := range ch {
		got += c.Content
	}
	if apiKey != "ant-key" || version != "2023-06-01" {
		t.Fatalf("headers apiKey=%q version=%q", apiKey, version)
	}
	if body["system"] != "be helpful" {
		t.Fatalf("system=%v", body["system"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages should exclude system, got %v", body["messages"])
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("content=%q", got)
	}
}

// T1.5.3 — 429 rate limit
func TestOpenAI_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `rate limit`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	b := cloud.NewOpenAI(srv.URL, "sk")
	_, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "x"}},
	})
	if !cloud.IsRateLimit(err) {
		t.Fatalf("expected rate limit error, got %v", err)
	}
	rl := err.(*cloud.RateLimitError)
	if rl.RetryAfter != 30*time.Second {
		t.Fatalf("retry-after=%v", rl.RetryAfter)
	}
}
