package backend_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/backend"
)

func TestParseOpenAIStreamPayload_ToolCalls(t *testing.T) {
	payload := `{"id":"chatcmpl-1","model":"codellama:7b","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]},"finish_reason":null}]}`
	chunk, ok := backend.ParseOpenAIStreamPayload(payload)
	if !ok {
		t.Fatal("expected ok")
	}
	if chunk.ID != "chatcmpl-1" || len(chunk.ToolCalls) != 1 {
		t.Fatalf("chunk=%+v", chunk)
	}
	tc := chunk.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Function == nil || tc.Function.Name != "read_file" {
		t.Fatalf("tool_call=%+v", tc)
	}
}

func TestParseOpenAIStreamPayload_Content(t *testing.T) {
	chunk, ok := backend.ParseOpenAIStreamPayload(`{"id":"1","choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`)
	if !ok || chunk.Content != "hi" {
		t.Fatalf("chunk=%+v ok=%v", chunk, ok)
	}
}

func TestParseOpenAIStreamPayload_Done(t *testing.T) {
	if _, ok := backend.ParseOpenAIStreamPayload("[DONE]"); ok {
		t.Fatal("DONE should not be ok")
	}
}

func TestMergeAndFinalizeToolCalls(t *testing.T) {
	var acc []backend.ToolCallDelta
	backend.MergeToolCallDeltas(&acc, []backend.ToolCallDelta{{
		Index: 0, ID: "call_1", Type: "function",
		Function: &backend.FunctionDelta{Name: "grep", Arguments: `{"q":`},
	}})
	backend.MergeToolCallDeltas(&acc, []backend.ToolCallDelta{{
		Index:    0,
		Function: &backend.FunctionDelta{Arguments: `"foo"}`},
	}})
	out := backend.FinalizeToolCalls(acc)
	if len(out) != 1 {
		t.Fatalf("out=%v", out)
	}
	fn := out[0]["function"].(map[string]any)
	if fn["name"] != "grep" || fn["arguments"] != `{"q":"foo"}` {
		t.Fatalf("fn=%v", fn)
	}
}
