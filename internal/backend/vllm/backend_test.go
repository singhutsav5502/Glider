package vllm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/vllm"
)

// T1.4.1 — Complete stream
func TestComplete_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"base\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	b := vllm.New(srv.URL)
	ch, err := b.Complete(context.Background(), &backend.CompletionRequest{
		Model:    "base",
		Messages: []backend.Message{{Role: "user", Content: "x"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for c := range ch {
		got += c.Content
	}
	if got != "hi" {
		t.Fatalf("got %q", got)
	}
}

// T1.4.2 — LoadAdapter
func TestLoadAdapter(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/load_lora_adapter" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := vllm.New(srv.URL)
	if err := b.LoadAdapter(context.Background(), "refactor-lora", "./adapters/refactor/"); err != nil {
		t.Fatal(err)
	}
	if got["lora_name"] != "refactor-lora" || got["lora_path"] != "./adapters/refactor/" {
		t.Fatalf("body=%v", got)
	}
}

// T1.4.3 — UnloadAdapter
func TestUnloadAdapter(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := vllm.New(srv.URL)
	if err := b.UnloadAdapter(context.Background(), "refactor-lora"); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/unload_lora_adapter" {
		t.Fatalf("path=%s", path)
	}
}

// T1.4.4 — Complete with adapter
func TestComplete_WithAdapter(t *testing.T) {
	var model string
	var adapterHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adapterHeader = r.Header.Get("X-LoRA-Adapter")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	b := vllm.New(srv.URL)
	req := &backend.CompletionRequest{
		Model:    "codellama-base",
		Messages: []backend.Message{{Role: "user", Content: "refactor"}},
		Metadata: backend.RequestMetadata{Adapter: "refactor-lora"},
	}
	ch, err := b.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if model != "refactor-lora" {
		t.Fatalf("model field=%q", model)
	}
	if adapterHeader != "refactor-lora" {
		t.Fatalf("header=%q", adapterHeader)
	}
}

// T1.2.2 interface compliance
func TestInterfaceCompliance(t *testing.T) {
	var _ backend.InferenceBackend = (*vllm.Backend)(nil)
	var _ backend.LoRAManager = (*vllm.Backend)(nil)
	_ = strings.Builder{}
}
