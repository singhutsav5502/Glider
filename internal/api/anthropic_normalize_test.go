package api_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/api"
)

func TestNormalizeGatewayModel(t *testing.T) {
	cases := map[string]string{
		"cus-claude-3.5-sonnet": "claude-3.5-sonnet",
		"CUS-gpt-4o":            "gpt-4o",
		"glider-local":          "local",
		"gpt-4o":                "gpt-4o",
	}
	for in, want := range cases {
		if got := api.NormalizeGatewayModel(in); got != want {
			t.Errorf("NormalizeGatewayModel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizeAnthropicShapedJSON(t *testing.T) {
	in := `{
		"model":"cus-claude-sonnet-4",
		"system":"be helpful",
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"name":"read_file","description":"read","input_schema":{"type":"object","additionalProperties":false,"title":"x","properties":{"path":{"type":"string"}}}}]
	}`
	out := api.NormalizeAnthropicShapedJSON([]byte(in))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "claude-sonnet-4" {
		t.Fatalf("model=%v", m["model"])
	}
	if _, ok := m["system"]; ok {
		t.Fatal("system should be folded into messages")
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("messages=%v", msgs)
	}
	user := msgs[1].(map[string]any)
	if user["content"] != "hi" {
		t.Fatalf("content=%v", user["content"])
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool=%v", tool)
	}
	fn := tool["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if _, ok := params["additionalProperties"]; ok {
		t.Fatal("additionalProperties should be stripped")
	}
}

func TestNormalizeAnthropic_ToolUseAndResult(t *testing.T) {
	in := `{
		"model":"cus-gpt-4o",
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"Reading"},
				{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"package main"}
			]}
		],
		"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{}}}]
	}`
	out := api.NormalizeAnthropicShapedJSON([]byte(in))
	req, err := api.ParseCompletionRequest(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages=%d want 2", len(req.Messages))
	}
	asst := req.Messages[0]
	if asst.Role != "assistant" || asst.Content != "Reading" || len(asst.ToolCalls) == 0 {
		t.Fatalf("assistant=%+v tool_calls=%s", asst, asst.ToolCalls)
	}
	var calls []map[string]any
	if err := json.Unmarshal(asst.ToolCalls, &calls); err != nil || len(calls) != 1 {
		t.Fatalf("tool_calls=%s err=%v", asst.ToolCalls, err)
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Fatalf("fn=%v", fn)
	}
	tool := req.Messages[1]
	if tool.Role != "tool" || tool.ToolCallID != "toolu_1" || tool.Content != "package main" {
		t.Fatalf("tool msg=%+v", tool)
	}
	if !req.HasTools() {
		t.Fatal("tools should be preserved")
	}
}

func TestParseCompletionRequest_PreservesToolLoopMessages(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"grep","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"hit"}
		]
	}`)
	req, err := api.ParseCompletionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len=%d", len(req.Messages))
	}
	if len(req.Messages[0].ToolCalls) == 0 {
		t.Fatal("assistant tool_calls dropped")
	}
	if req.Messages[1].ToolCallID != "call_1" || req.Messages[1].Content != "hit" {
		t.Fatalf("tool=%+v", req.Messages[1])
	}
}

