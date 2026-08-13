package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
)

type mockCompleter struct {
	chunks []backend.CompletionChunk
	err    error
	last   *backend.CompletionRequest
}

func (m *mockCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan backend.CompletionChunk, len(m.chunks))
	for _, c := range m.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

type mockModels struct{ ids []string }

func (m mockModels) ListModelIDs() []string { return m.ids }

// T1.1.1 — Parse valid request
func TestParseValidCompletionRequest(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	req, err := api.ParseCompletionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-4o" || !req.Stream || len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
		t.Fatalf("parsed=%+v", req)
	}
}

func TestParseCompletionRequest_PreservesTools(t *testing.T) {
	body := []byte(`{
		"model":"cus-codellama:7b",
		"messages":[{"role":"user","content":"read it"}],
		"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],
		"tool_choice":"auto"
	}`)
	norm := api.NormalizeAnthropicShapedJSON(body)
	req, err := api.ParseCompletionRequest(norm)
	if err != nil {
		t.Fatal(err)
	}
	if !req.HasTools() {
		t.Fatal("expected tools preserved")
	}
	var tools []map[string]any
	if err := json.Unmarshal(req.Tools, &tools); err != nil || len(tools) != 1 {
		t.Fatalf("tools=%s err=%v", req.Tools, err)
	}
	var tc string
	if err := json.Unmarshal(req.ToolChoice, &tc); err != nil || tc != "auto" {
		t.Fatalf("tool_choice=%s err=%v", req.ToolChoice, err)
	}
}

// T1.1.2 — Reject malformed request
func TestRejectMissingMessages(t *testing.T) {
	h := &api.Handlers{Completer: &mockCompleter{}}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	errObj, _ := body["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("body=%v", body)
	}
}

// T1.1.3 — Stream SSE
func TestStreamSSE(t *testing.T) {
	mc := &mockCompleter{chunks: []backend.CompletionChunk{
		{Content: "Hello"},
		{Content: " world"},
		{Content: "!"},
	}}
	h := &api.Handlers{Completer: mc}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%s", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	text := string(raw)
	if !strings.Contains(text, "data: ") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("body=%s", text)
	}
	lines := strings.Split(text, "\n")
	dataLines := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") {
			dataLines++
			var chunk map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
				t.Fatalf("invalid json chunk: %v", err)
			}
			if chunk["object"] != "chat.completion.chunk" {
				t.Fatalf("object=%v", chunk["object"])
			}
		}
	}
	if dataLines != 3 {
		t.Fatalf("expected 3 data chunks, got %d in %s", dataLines, text)
	}
	if !strings.Contains(text, "Hello") || !strings.Contains(text, " world") || !strings.Contains(text, "!") {
		t.Fatalf("missing content in %s", text)
	}
}

// T1.1.4 — Non-streaming
func TestNonStreaming(t *testing.T) {
	mc := &mockCompleter{chunks: []backend.CompletionChunk{
		{Content: "full"},
		{Content: " reply"},
	}}
	h := &api.Handlers{Completer: mc}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["object"] != "chat.completion" {
		t.Fatalf("body=%v", body)
	}
	choices, _ := body["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "full reply" {
		t.Fatalf("content=%v", msg["content"])
	}
}

// T1.1.5 — List models
func TestListModels(t *testing.T) {
	h := &api.Handlers{Models: mockModels{ids: []string{"a", "b", "c"}}}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	data, _ := body["data"].([]any)
	if len(data) != 3 {
		t.Fatalf("data=%v", data)
	}
}

// T1.1.6 — Request ID propagation
func TestRequestID(t *testing.T) {
	mc := &mockCompleter{chunks: []backend.CompletionChunk{{Content: "x"}}}
	h := &api.Handlers{Completer: mc}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()

	ids := map[string]bool{}
	for i := 0; i < 3; i++ {
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
			bytes.NewReader([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
		if err != nil {
			t.Fatal(err)
		}
		id := resp.Header.Get("X-Request-ID")
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if id == "" {
			t.Fatal("missing X-Request-ID")
		}
		if ids[id] {
			t.Fatalf("duplicate id %s", id)
		}
		ids[id] = true
		if !strings.Contains(string(raw), id) {
			t.Fatalf("request id not in SSE chunks: header=%s body=%s", id, raw)
		}
	}
}

func TestParseCompletionRequest_Context(_ *testing.T) {
	_ = context.Background()
}

func TestWriteChatSSE_EmitsToolCalls(t *testing.T) {
	h := &api.Handlers{Completer: &mockCompleter{chunks: []backend.CompletionChunk{
		{
			ID: "chatcmpl-tc",
			ToolCalls: []backend.ToolCallDelta{{
				Index: 0, ID: "call_1", Type: "function",
				Function: &backend.FunctionDelta{Name: "read_file", Arguments: `{"path":"a.go"}`},
			}},
			FinishReason: "tool_calls",
			Model:        "codellama:7b",
		},
	}}}
	srv := httptest.NewServer(api.NewServer("", h).Handler())
	defer srv.Close()
	body := `{"model":"cus-codellama:7b","messages":[{"role":"user","content":"read"}],"stream":true,"tools":[{"type":"function","function":{"name":"read_file"}}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `read_file`) {
		t.Fatalf("SSE missing tool_calls: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"tool_calls"`) {
		t.Fatalf("SSE missing finish_reason tool_calls: %s", s)
	}
}
