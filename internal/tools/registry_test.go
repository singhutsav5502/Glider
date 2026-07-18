package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/mcp"
)

func TestStandardBuiltins(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Hello() {}\n"), 0o644)
	r := NewRegistry(Options{Workspace: dir, AllowShell: false})
	names := map[string]bool{}
	for _, s := range r.Catalog(context.Background()) {
		names[s.Name] = true
	}
	for _, n := range []string{"fs_read", "code_grep", "git_status", "datetime", "calculator", "context_query"} {
		if !names[n] {
			t.Fatalf("missing %s in catalog", n)
		}
	}
	res, err := r.Invoke(context.Background(), Ref{Name: "fs_list", Kind: KindBuiltin}, ".")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "code_grep", Kind: KindBuiltin}, "Hello")
	if err != nil || !res.OK || res.Output == "" {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "calculator", Kind: KindBuiltin}, "3*7")
	if err != nil || res.Output != "21" {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "datetime", Kind: KindBuiltin}, "")
	if err != nil || !res.OK {
		t.Fatalf("%+v", res)
	}
}

func TestMCPViaRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "m"}},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "get_me", "description": "me"}}},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "login=glider"}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		}
	}))
	defer srv.Close()

	mgr := mcp.NewManager()
	_, err := mgr.Connect(context.Background(), mcp.ServerConfig{
		ID: "github", Transport: mcp.TransportHTTP, URL: srv.URL,
		Auth: mcp.AuthConfig{Kind: mcp.AuthNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(Options{MCP: mgr})
	res, err := r.Invoke(context.Background(), Ref{Name: "get_me", Kind: KindMCP, Server: "github"}, "")
	if err != nil || !res.OK || res.Stubbed {
		t.Fatalf("%+v err=%v", res, err)
	}
	if res.Output != "login=glider" {
		t.Fatalf("output=%q", res.Output)
	}
}

func TestAgentLoopAndOpenAITools(t *testing.T) {
	r := NewRegistry(Options{})
	refs := []Ref{{Name: "calculator", Kind: KindBuiltin}}
	raw := r.OpenAIToolsJSON(context.Background(), refs)
	if !strings.Contains(string(raw), "calculator") {
		t.Fatalf("tools json=%s", raw)
	}
	out, err := r.RunAgentLoop(context.Background(), "sys", "compute 2+2", AgentLoopOpts{
		Refs: refs, MaxSteps: 3,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			// First turn: request tool; second: final answer.
			for _, m := range messages {
				if m["role"] == "tool" {
					return "answer is 4", nil, nil
				}
			}
			return "", []ToolCallDelta{{ID: "1", Name: "calculator", Arguments: `{"expr":"2+2"}`}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "answer is 4" || len(out.Results) != 1 || out.Results[0].Output != "4" {
		t.Fatalf("%+v", out)
	}
	par := r.InvokeAllParallel(context.Background(), refs, "1+1")
	if len(par) != 1 || par[0].Output != "2" {
		t.Fatalf("%+v", par)
	}
}
