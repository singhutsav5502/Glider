package e2e_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// T1.7.1 — Full round-trip Proxy → Ollama → Proxy
func TestE2E_ProxyToOllama(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{"A", "B", "C"}
		for i, c := range chunks {
			_, _ = io.WriteString(w, `data: {"id":"1","model":"llama3","choices":[{"delta":{"content":"`+c+`"}}]}`+"\n\n")
			flusher.Flush()
			_ = i
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ollamaSrv.Close()

	reg := backend.NewRegistry()
	_ = reg.Register(ollama.New(ollamaSrv.URL))
	_ = reg.RegisterModel(backend.ModelInfo{Name: "llama3"})

	completer := &orchestrator.PassthroughCompleter{Registry: reg, BackendName: "ollama", Model: "llama3"}
	h := &api.Handlers{Completer: completer, Models: orchestrator.RegistryModelLister{Registry: reg}}
	proxy := httptest.NewServer(api.NewServer("", h).Handler())
	defer proxy.Close()

	start := time.Now()
	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"llama3","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	text := string(raw)
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("ct=%s", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(text, `"content":"A"`) || !strings.Contains(text, `"content":"B"`) || !strings.Contains(text, `"content":"C"`) {
		t.Fatalf("body=%s", text)
	}
	if !strings.Contains(text, "[DONE]") {
		t.Fatalf("missing DONE: %s", text)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("too slow: %v", elapsed)
	}
}

// T1.7.2 — Full round-trip Proxy → Cloud → Proxy
func TestE2E_ProxyToCloud(t *testing.T) {
	var auth string
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"1","model":"gpt-4o","choices":[{"delta":{"content":"cloud"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer cloudSrv.Close()

	reg := backend.NewRegistry()
	_ = reg.Register(cloud.NewOpenAI(cloudSrv.URL, "sk-e2e"))
	completer := &orchestrator.PassthroughCompleter{Registry: reg, BackendName: "openai", Model: "gpt-4o"}
	h := &api.Handlers{Completer: completer}
	proxy := httptest.NewServer(api.NewServer("", h).Handler())
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if auth != "Bearer sk-e2e" {
		t.Fatalf("auth=%q", auth)
	}
	if !strings.Contains(string(raw), "cloud") || !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("body=%s", raw)
	}
}
