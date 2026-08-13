package backend_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
)

func TestHasToolsAndAttachTools(t *testing.T) {
	req := &backend.CompletionRequest{}
	if req.HasTools() {
		t.Fatal("empty should be false")
	}
	req.Tools = json.RawMessage(`[]`)
	if req.HasTools() {
		t.Fatal("empty array should be false")
	}
	req.Tools = json.RawMessage(`[{"type":"function","function":{"name":"read_file"}}]`)
	req.ToolChoice = json.RawMessage(`"auto"`)
	if !req.HasTools() {
		t.Fatal("expected tools")
	}
	names := req.ToolNames()
	if len(names) != 1 || names[0] != "read_file" {
		t.Fatalf("ToolNames=%v", names)
	}
	body := map[string]any{"model": "m"}
	backend.AttachTools(body, req)
	if body["tools"] == nil {
		t.Fatal("tools not attached")
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice=%v", body["tool_choice"])
	}
}

func TestAttachFormat(t *testing.T) {
	body := map[string]any{"model": "m"}
	backend.AttachFormat(body, &backend.CompletionRequest{Format: backend.CriticEvalFormatJSON()})
	if body["format"] != "json" {
		t.Fatalf("format=%v", body["format"])
	}
	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Fatalf("response_format=%v", body["response_format"])
	}

	body2 := map[string]any{"model": "m"}
	backend.AttachFormat(body2, &backend.CompletionRequest{Format: backend.CriticEvalFormat()})
	if _, ok := body2["format"].(map[string]any); !ok {
		t.Fatalf("schema format=%#v", body2["format"])
	}
	rf2, _ := body2["response_format"].(map[string]any)
	if rf2["type"] != "json_schema" {
		t.Fatalf("response_format=%v", body2["response_format"])
	}
	if !backend.FormatIsJSONMode(backend.CriticEvalFormatJSON()) {
		t.Fatal("expected json mode")
	}
	if backend.FormatIsJSONMode(backend.CriticEvalFormat()) {
		t.Fatal("schema is not json mode")
	}
}

func TestIsToolsUnsupported(t *testing.T) {
	if !backend.IsToolsUnsupported(&backend.ToolsUnsupportedError{Backend: "ollama", Message: "nope"}) {
		t.Fatal("typed error")
	}
	if !backend.IsToolsUnsupported(errString("ollama error: status 400: does not support tools")) {
		t.Fatal("string detect")
	}
	if backend.IsToolsUnsupported(errString("connection refused")) {
		t.Fatal("false positive")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
