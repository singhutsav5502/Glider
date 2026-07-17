package ollama_test

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
	"github.com/glider-ai/glider/internal/backend/ollama"
)

// T1.2.1 compile-time check lives in backend.go

// T1.3.1 — Complete: stream response from Ollama
func TestComplete_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"id":"1","model":"llama3","choices":[{"delta":{"content":"Hello"}}]}`,
			`{"id":"1","model":"llama3","choices":[{"delta":{"content":" world"}}]}`,
			`{"id":"1","model":"llama3","choices":[{"delta":{"content":"!"},"finish_reason":"stop"}]}`,
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	ch, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model:    "llama3",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var content strings.Builder
	count := 0
	for chunk := range ch {
		content.WriteString(chunk.Content)
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 chunks, got %d", count)
	}
	if content.String() != "Hello world!" {
		t.Fatalf("content=%q", content.String())
	}
}

// T1.3.2 — Complete: handle Ollama error
func TestComplete_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	ch, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model:    "llama3",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil || ch != nil {
		t.Fatalf("expected error, got ch=%v err=%v", ch, err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention status: %v", err)
	}
}

// T1.3.3 — Complete: handle connection refused
func TestComplete_ConnectionRefused(t *testing.T) {
	b := ollama.New("http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := b.Complete(ctx, &backend.CompletionRequest{
		Model:    "llama3",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("hung too long")
	}
}

// T1.3.4 — LoadModel
func TestLoadModel(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	err := b.LoadModel(context.Background(), "llama3:8b", backend.LoadOptions{KeepAlive: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "llama3:8b" {
		t.Fatalf("model=%v", gotBody["model"])
	}
	if gotBody["keep_alive"] != "30m" {
		t.Fatalf("keep_alive=%v", gotBody["keep_alive"])
	}
}

// T1.3.5 — UnloadModel
func TestUnloadModel(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	if err := b.UnloadModel(context.Background(), "llama3:8b"); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "llama3:8b" {
		t.Fatalf("model=%v", gotBody["model"])
	}
	// JSON numbers decode as float64
	if gotBody["keep_alive"] != float64(0) {
		t.Fatalf("keep_alive=%v", gotBody["keep_alive"])
	}
}

// T1.3.6 — ListLoaded
func TestListLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3:8b","size_vram":5000000000}]}`))
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	models, err := b.ListLoaded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "llama3:8b" || models[0].SizeVRAM != 5000000000 {
		t.Fatalf("got %+v", models)
	}
}

// T1.3.7 — HealthCheck ping
func TestPing_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := ollama.New(srv.URL)
	if err := b.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.IsHealthy() {
		t.Fatal("expected healthy")
	}
}

// T1.3.8 — HealthCheck down
func TestPing_Down(t *testing.T) {
	b := ollama.New("http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.Ping(ctx); err == nil {
		t.Fatal("expected error")
	}
	if b.IsHealthy() {
		t.Fatal("expected unhealthy")
	}
}
