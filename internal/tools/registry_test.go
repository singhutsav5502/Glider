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

	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/mcp"
)

func TestResolveWorkspace(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		// Abs may normalize; compare Abs forms.
		want, _ := filepath.Abs(dir)
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	empty, err := ResolveWorkspace("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(empty), ".glider/workspace") && !strings.Contains(filepath.ToSlash(empty), `.glider\workspace`) {
		// Windows uses backslash; ToSlash handles it.
		if !strings.Contains(filepath.ToSlash(empty), ".glider/workspace") {
			t.Fatalf("default workspace=%q", empty)
		}
	}
	st, err := os.Stat(empty)
	if err != nil || !st.IsDir() {
		t.Fatalf("default workspace not created: %v", err)
	}
}

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

func TestFlattenToolArgsPathContent(t *testing.T) {
	got := FlattenToolArgs("fs_write", `{"path":"a.txt","content":"hello"}`)
	if got != "a.txt\nhello" {
		t.Fatalf("got %q", got)
	}
	got = FlattenToolArgs("artifact_write", `{"kind":"out","path":"r.md","content":"# hi"}`)
	if got != "out r.md\n# hi" {
		t.Fatalf("got %q", got)
	}
	got = FlattenToolArgs("git_clone", `{"url":"https://example.com/r.git","dir":"dest"}`)
	if got != "https://example.com/r.git dest" {
		t.Fatalf("got %q", got)
	}
	got = FlattenToolArgs("git_clone", `{"url":"https://example.com/r.git","targetDir":"audit-target"}`)
	if got != "https://example.com/r.git audit-target" {
		t.Fatalf("targetDir alias got %q", got)
	}
}

func TestParseTextToolCalls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // names
	}{
		{
			name: "bare object",
			in:   `{"name":"git_clone","arguments":{"url":"https://github.com/o/r.git","targetDir":"audit-target"}}`,
			want: []string{"git_clone"},
		},
		{
			name: "fenced json",
			in:   "Sure.\n```json\n{\"name\":\"fs_list\",\"arguments\":{\"path\":\"audit-target\"}}\n```\n",
			want: []string{"fs_list"},
		},
		{
			name: "array",
			in:   `[{"name":"fs_list","arguments":{"path":"."}},{"name":"code_grep","arguments":{"pattern":"TODO"}}]`,
			want: []string{"fs_list", "code_grep"},
		},
		{
			name: "openai function envelope",
			in:   `{"type":"function","function":{"name":"datetime","arguments":"{}"}}`,
			want: []string{"datetime"},
		},
		{
			name: "nested plan steps",
			in:   `{"name":"plan","arguments":{"steps":[{"name":"git_clone","arguments":{"url":"https://x/y.git","dir":"audit-target"}},{"name":"fs_list","arguments":{"path":"audit-target"}}]}}`,
			want: []string{"git_clone", "fs_list"},
		},
		{
			name: "prose wrapped",
			in:   "I will clone now:\n{\"name\": \"git_clone\", \"arguments\": {\"url\": \"https://example.com/r.git\", \"dir\": \"dest\"}}\n",
			want: []string{"git_clone"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTextToolCalls(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d: %+v", len(got), len(tc.want), got)
			}
			for i, name := range tc.want {
				if got[i].Name != name {
					t.Fatalf("[%d] name=%q want %q", i, got[i].Name, name)
				}
			}
		})
	}
	// Bare plan with no tool steps must not invent a "plan" tool call.
	if got := ParseTextToolCalls(`{"name":"plan","arguments":{"steps":["clone repo","audit"]}}`); len(got) != 0 {
		t.Fatalf("unexpected calls from textual plan: %+v", got)
	}
}

func TestAgentLoopParsesTextJSONToolCalls(t *testing.T) {
	r := NewRegistry(Options{})
	refs := []Ref{{Name: "calculator", Kind: KindBuiltin}}
	out, err := r.RunAgentLoop(context.Background(), "sys", "compute", AgentLoopOpts{
		Refs: refs, MaxSteps: 3,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			for _, m := range messages {
				if m["role"] == "tool" {
					return "done via text json tools", nil, nil
				}
			}
			// Model printed tool JSON as text — no structured tool_calls.
			return `{"name":"calculator","arguments":{"expr":"3+4"}}`, nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "done via text json tools" || len(out.Results) != 1 || out.Results[0].Output != "7" {
		t.Fatalf("%+v", out)
	}
}

func TestAgentLoopRejectsToolsNotInRefs(t *testing.T) {
	r := NewRegistry(Options{Workspace: t.TempDir()})
	refs := []Ref{{Name: "fs_list", Kind: KindBuiltin}}
	out, err := r.RunAgentLoop(context.Background(), "sys", "audit", AgentLoopOpts{
		Refs: refs, MaxSteps: 2,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			for _, m := range messages {
				if m["role"] == "tool" {
					return "skipped undeclared clone", nil, nil
				}
			}
			return `{"name":"git_clone","arguments":{"url":"https://example.com/r.git","dir":"audit-target"}}`, nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "skipped undeclared clone" || len(out.Results) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Results[0].OK || !strings.Contains(out.Results[0].Err, "not allowed") {
		t.Fatalf("expected reject, got %+v", out.Results[0])
	}
}

func TestAgentLoopBudgetMessage(t *testing.T) {
	r := NewRegistry(Options{})
	refs := []Ref{{Name: "calculator", Kind: KindBuiltin}}
	out, err := r.RunAgentLoop(context.Background(), "sys", "loop", AgentLoopOpts{
		Refs: refs, MaxSteps: 2,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			return "", []ToolCallDelta{{ID: "1", Name: "calculator", Arguments: `{"expr":"1+1"}`}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "tool loop budget exhausted after 2 steps") {
		t.Fatalf("budget text=%q", out.Text)
	}
}

func TestArtifactWriteJSONArgs(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Options{Workspace: dir})
	_, err := r.EnsureRunLayout("t1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Invoke(context.Background(), Ref{Name: "artifact_write", Kind: KindBuiltin},
		`{"kind":"out","path":"note.txt","content":"hello artifact"}`)
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	p := filepath.Join(dir, "runs", "t1", "out", "note.txt")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "hello artifact" {
		t.Fatalf("file=%q err=%v", b, err)
	}
}

func TestReadFileAndWalk(t *testing.T) {
	dir := t.TempDir()
	lay := LayoutForRun(dir, "r1")
	if err := lay.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(lay.OutAbs, "x.txt")
	if err := os.WriteFile(path, []byte("preview-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, trunc, size, err := ReadFileCapped(dir, lay.RelOut+"/x.txt", 64)
	if err != nil || trunc || size != 10 || content != "preview-me" {
		t.Fatalf("content=%q trunc=%v size=%d err=%v", content, trunc, size, err)
	}
	files, err := WalkFiles(dir, lay.RelOut, 10)
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
}

func TestBlindSafeAndExpandRefs(t *testing.T) {
	if !BlindSafe(Ref{Name: "fs_list"}) {
		t.Fatal("fs_list should be blind-safe")
	}
	if BlindSafe(Ref{Name: "git_clone"}) {
		t.Fatal("git_clone must not be blind-invoked")
	}
	if BlindSafe(Ref{Name: "fs_write"}) || BlindSafe(Ref{Name: "artifact_write"}) {
		t.Fatal("write tools must not be blind-invoked with the goal")
	}
	if BlindSafe(Ref{Name: "code_grep"}) || BlindSafe(Ref{Name: "fs_search"}) || BlindSafe(Ref{Name: "context_query"}) {
		t.Fatal("query tools must not be blind-invoked with goal prose")
	}
	if BlindSafe(Ref{Name: "web_search"}) || BlindSafe(Ref{Name: "web_fetch"}) || BlindSafe(Ref{Name: "http_fetch"}) {
		t.Fatal("web/http tools must not be blind-invoked")
	}
	if BlindSafe(Ref{Name: "get_me", Kind: KindMCP, Server: "github"}) {
		t.Fatal("mcp tools must not be blind-invoked")
	}
	if BlindSafe(Ref{Name: "*", Kind: KindMCP, Server: "github"}) {
		t.Fatal("MCP * must ExpandRefs before invoke; not blind-safe as *")
	}
	if BlindPrepassInput() != "." {
		t.Fatalf("BlindPrepassInput=%q want .", BlindPrepassInput())
	}
	r := NewRegistry(Options{})
	expanded := r.ExpandRefs(context.Background(), []Ref{
		{Name: "*", Kind: KindMCP, Server: "github"},
		{Name: "fs_read", Kind: KindBuiltin},
	})
	// No MCP client → keep list_tools probe + fs_read
	if len(expanded) != 2 || expanded[0].Name != "list_tools" || expanded[1].Name != "fs_read" {
		t.Fatalf("%+v", expanded)
	}
	// Kind omitted but server set → still expand away from *
	expanded2 := r.ExpandRefs(context.Background(), []Ref{
		{Name: "*", Server: "github"},
	})
	if len(expanded2) != 1 || expanded2[0].Name != "list_tools" {
		t.Fatalf("kind-omitted *: %+v", expanded2)
	}
	// Bare * with no server is dropped (never CallTool)
	expanded3 := r.ExpandRefs(context.Background(), []Ref{
		{Name: "*", Kind: KindMCP},
	})
	if len(expanded3) != 0 {
		t.Fatalf("bare *: %+v", expanded3)
	}
	blind := FilterBlindSafe(append(expanded, Ref{Name: "fs_write"}, Ref{Name: "code_grep"}))
	for _, ref := range blind {
		if ref.Name == "fs_write" || ref.Name == "code_grep" || ref.Name == "*" {
			t.Fatalf("FilterBlindSafe leaked %+v", blind)
		}
	}
}

func TestWriteToolsRejectGoalProse(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Options{Workspace: dir})
	goal := "Clone a target repository into the workspace, then produce a structured audit covering\ncode quality and security."
	res, err := r.Invoke(context.Background(), Ref{Name: "fs_write"}, goal)
	if err == nil || res.OK {
		t.Fatalf("fs_write should reject goal prose: %+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "artifact_write"}, goal)
	if err == nil || res.OK {
		t.Fatalf("artifact_write should reject goal prose: %+v err=%v", res, err)
	}
	// Blind-safe list with prose falls back to workspace root.
	res, err = r.Invoke(context.Background(), Ref{Name: "fs_list"}, goal)
	if err != nil || !res.OK {
		t.Fatalf("fs_list prose→root: %+v err=%v", res, err)
	}
}

func TestInvokeAllParallelExpandsStar(t *testing.T) {
	r := NewRegistry(Options{})
	par := r.InvokeAllParallel(context.Background(), []Ref{
		{Name: "*", Kind: KindMCP, Server: "github"},
		{Name: "datetime", Kind: KindBuiltin},
	}, "goal text here")
	for _, res := range par {
		if res.Name == "*" {
			t.Fatalf("InvokeAllParallel must not leave *: %+v", par)
		}
		if strings.Contains(res.Err, "unknown tool") {
			t.Fatalf("must not CallTool(*): %+v", res)
		}
	}
}

func TestInvokeStarUsesListToolsNotCallTool(t *testing.T) {
	var callToolNames []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     any             `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "m"}},
			})
		case "notifications/initialized":
			w.WriteHeader(204)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "get_me", "description": "me"}}},
			})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			callToolNames = append(callToolNames, p.Name)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}, "isError": false},
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
	res, err := r.Invoke(context.Background(), Ref{Name: "*", Kind: KindMCP, Server: "github"}, "goal")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if res.Name != "list_tools" {
		t.Fatalf("want list_tools result name, got %+v", res)
	}
	for _, n := range callToolNames {
		if n == "*" {
			t.Fatalf("CallTool(*) must never happen: %v", callToolNames)
		}
	}
	raw := string(r.OpenAIToolsJSON(context.Background(), []Ref{{Name: "*", Kind: KindMCP, Server: "github"}}))
	if strings.Contains(raw, `"name":"*"`) || strings.Contains(raw, `"name": "*"`) {
		t.Fatalf("OpenAI tools must not advertise *: %s", raw)
	}
}

func TestFSWriteFromFlattenedJSON(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Options{Workspace: dir})
	input := FlattenToolArgs("fs_write", `{"path":"w.txt","content":"from-loop"}`)
	res, err := r.Invoke(context.Background(), Ref{Name: "fs_write"}, input)
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "w.txt"))
	if string(b) != "from-loop" {
		t.Fatalf("%q", b)
	}
}

func TestScopeRelMatchesGitClone(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Options{Workspace: dir})
	if got := r.ScopeRel("audit-target"); got != "audit-target" {
		t.Fatalf("no run id: got %q", got)
	}
	if _, err := r.EnsureRunLayout("clone-repo-security-audit"); err != nil {
		t.Fatal(err)
	}
	want := "runs/clone-repo-security-audit/work/audit-target"
	if got := r.ScopeRel("audit-target"); got != want {
		t.Fatalf("ScopeRel=%q want %q", got, want)
	}
	if got := r.ScopeRel("."); got != "runs/clone-repo-security-audit/work" {
		t.Fatalf("ScopeRel(.)=%q", got)
	}
	if got := r.ScopeRel("runs/other/work/x"); got != "runs/other/work/x" {
		t.Fatalf("runs/ passthrough=%q", got)
	}

	// Simulate successful clone path + fs_list bare audit-target (the HITL confusion case).
	cloneDest := filepath.Join(dir, "runs", "clone-repo-security-audit", "work", "audit-target")
	if err := os.MkdirAll(filepath.Join(cloneDest, "frontend", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneDest, "README.md"), []byte("# ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Workspace-root audit-target must NOT exist (disk evidence from the bad HITL run).
	if _, err := os.Stat(filepath.Join(dir, "audit-target")); !os.IsNotExist(err) {
		t.Fatalf("root audit-target should be absent: %v", err)
	}
	list, err := r.Invoke(context.Background(), Ref{Name: "fs_list"}, "audit-target")
	if err != nil || !list.OK {
		t.Fatalf("fs_list bare audit-target: %+v err=%v", list, err)
	}
	if !strings.Contains(list.Output, "README.md") || !strings.Contains(list.Output, "frontend") {
		t.Fatalf("expected clone tree, got %q", list.Output)
	}
	read, err := r.Invoke(context.Background(), Ref{Name: "fs_read"}, "audit-target/README.md")
	if err != nil || !read.OK || !strings.Contains(read.Output, "# ok") {
		t.Fatalf("fs_read: %+v err=%v", read, err)
	}
	grep, err := r.Invoke(context.Background(), Ref{Name: "code_grep"}, "# ok")
	if err != nil || !grep.OK || !strings.Contains(grep.Output, "audit-target/README.md") {
		t.Fatalf("code_grep under run work: %+v err=%v", grep, err)
	}
}

func TestContextQueryHoopKeys(t *testing.T) {
	store := contextgraph.New("")
	turn := "loop:clone-repo-security-audit"
	store.RecordHoopContext(turn, contextgraph.HoopKeyClonePath, "runs/clone-repo-security-audit/work/audit-target")
	store.RecordHoopContext(turn, contextgraph.HoopKeyGoal, "security audit")
	r := NewRegistry(Options{
		Workspace: t.TempDir(),
		Context:   contextgraph.ContextQuerier{Store: store},
	})
	res, err := r.Invoke(context.Background(), Ref{Name: "context_query"}, turn+" key=clone_path")
	if err != nil || !res.OK || !strings.Contains(res.Output, "audit-target") {
		t.Fatalf("key=clone_path: %+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "context_query"}, turn+" goal OR plan OR clone_path")
	if err != nil || !res.OK || !strings.Contains(res.Output, "clone_path") {
		t.Fatalf("OR query: %+v err=%v", res, err)
	}
}

func TestGitCloneAndFsListSameRelWork(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Options{Workspace: dir})
	if _, err := r.EnsureRunLayout("hoop1"); err != nil {
		t.Fatal(err)
	}
	// Pretend clone already wrote RelWork/audit-target (avoid network).
	dest := filepath.Join(dir, "runs", "hoop1", "work", "audit-target")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.Invoke(context.Background(), Ref{Name: "fs_list"}, "audit-target")
	if err != nil || !res.OK || !strings.Contains(res.Output, "a.go") {
		t.Fatalf("%+v err=%v", res, err)
	}
}
