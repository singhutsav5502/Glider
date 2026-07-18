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

