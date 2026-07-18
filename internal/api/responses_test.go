package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
)

func TestLooksLikeResponses(t *testing.T) {
	if !api.LooksLikeResponses([]byte(`{"model":"gpt-4o","input":"hi"}`)) {
		t.Fatal("expected responses shape")
	}
	if api.LooksLikeResponses([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("chat completions should not look like responses")
	}
}

func TestResponsesToCompletionStringInput(t *testing.T) {
	req, err := api.ResponsesToCompletion([]byte(`{
		"model":"gpt-4o",
		"input":"hello",
		"instructions":"be brief",
		"stream":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-4o" || !req.Stream {
		t.Fatalf("%+v", req)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "hello" {
		t.Fatalf("messages=%+v", req.Messages)
	}
}

func TestResponsesHandler(t *testing.T) {
	h := &api.Handlers{Completer: &stubCompleter{text: "pong"}}
	srv := api.NewServer(":0", h)
	body := `{"model":"gpt-4o","input":"ping"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["object"] != "response" {
		t.Fatalf("%v", resp)
	}
}

func TestChatCompletionsAcceptsResponsesBody(t *testing.T) {
	h := &api.Handlers{Completer: &stubCompleter{text: "ok"}}
	srv := api.NewServer(":0", h)
	body := `{"model":"gpt-4o","input":"hi"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "response") {
		t.Fatalf("expected responses-shaped output, got %s", rr.Body.String())
	}
}

type stubCompleter struct{ text string }

func (s *stubCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: s.text, FinishReason: "stop", Model: req.Model}
	close(ch)
	return ch, nil
}
