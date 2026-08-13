package ngl_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// Content-block shapes below match planning/agent_cli_interop.md §1's
// captured findings verbatim.

func TestParseClaudeAssistantParts_TextAndToolUse(t *testing.T) {
	content := []byte(`[
		{"type":"text","text":"I'll edit that file now."},
		{"type":"tool_use","id":"toolu_01abc","name":"Edit","input":{"file_path":"math_utils.py","old_string":"def add(a, b)","new_string":"def add(a, b, c=0)"}}
	]`)
	parts, err := ngl.ParseClaudeAssistantParts(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].Kind != ngl.PartUserText || parts[0].Text != "I'll edit that file now." {
		t.Fatalf("got %+v", parts[0])
	}
	if parts[1].Kind != ngl.PartToolCall || parts[1].ToolCall == nil {
		t.Fatalf("got %+v", parts[1])
	}
	if parts[1].ToolCall.ID != "toolu_01abc" || parts[1].ToolCall.Name != "Edit" {
		t.Fatalf("got ToolCall %+v", parts[1].ToolCall)
	}
}

func TestParseClaudeToolResult(t *testing.T) {
	block := []byte(`{"type":"tool_result","tool_use_id":"toolu_01abc","content":"File edited successfully"}`)
	tr, err := ngl.ParseClaudeToolResult(block)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.ToolCallID != "toolu_01abc" || tr.Text != "File edited successfully" || tr.IsError {
		t.Fatalf("got %+v", tr)
	}
}

// TestClaudeEditViews_EditProducesHunks matches the documented Edit
// structuredPatch shape exactly: {oldStart,oldLines,newStart,newLines,
// lines:[" ctx","-old","+new"]} plus oldString/newString/originalFile.
func TestClaudeEditViews_EditProducesHunks(t *testing.T) {
	result := []byte(`{
		"filePath": "math_utils.py",
		"oldString": "def add(a, b):",
		"newString": "def add(a, b, c=0):",
		"originalFile": "def add(a, b):\n    return a + b\n",
		"structuredPatch": [
			{"oldStart": 1, "oldLines": 2, "newStart": 1, "newLines": 2, "lines": [" ctx", "-def add(a, b):", "+def add(a, b, c=0):"]}
		]
	}`)
	views, err := ngl.ClaudeEditViews(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if views.Path != "math_utils.py" {
		t.Fatalf("got path %q", views.Path)
	}
	if views.WholeFile != nil {
		t.Fatalf("Edit result must not populate WholeFile")
	}
	if len(views.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(views.Hunks))
	}
	h := views.Hunks[0]
	if h.OldStart != 1 || h.OldLines != 2 || h.NewStart != 1 || h.NewLines != 2 {
		t.Fatalf("got hunk %+v", h)
	}
	if len(h.Lines) != 3 || h.Lines[1] != "-def add(a, b):" {
		t.Fatalf("got lines %+v", h.Lines)
	}
}

// TestClaudeEditViews_WriteProducesWholeFile matches the documented Write
// result shape exactly: reuses Edit's schema with structuredPatch:[] and
// originalFile:null as the "this was a create/overwrite" convention.
func TestClaudeEditViews_WriteProducesWholeFile(t *testing.T) {
	result := []byte(`{
		"type": "create",
		"filePath": "new_module.py",
		"content": "def hello():\n    print('hi')\n",
		"structuredPatch": [],
		"originalFile": null,
		"userModified": false
	}`)
	views, err := ngl.ClaudeEditViews(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if views.Hunks != nil {
		t.Fatalf("Write result must not populate Hunks, got %+v", views.Hunks)
	}
	if views.WholeFile == nil {
		t.Fatalf("expected WholeFile to be populated")
	}
	if views.WholeFile.Content != "def hello():\n    print('hi')\n" {
		t.Fatalf("got content %q", views.WholeFile.Content)
	}
}

func TestParseClaudeAssistantParts_HostedWebSearchDetection(t *testing.T) {
	content := []byte(`[
		{"type":"tool_use","id":"toolu_01local","name":"Read","input":{"file_path":"x.py"}},
		{"type":"tool_use","id":"srvtoolu_01hosted","name":"WebSearch","input":{"query":"golang generics"}}
	]`)
	parts, err := ngl.ParseClaudeAssistantParts(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parts[0].ToolCall.Hosted {
		t.Fatalf("toolu_ prefixed call must not be marked Hosted")
	}
	if !parts[1].ToolCall.Hosted {
		t.Fatalf("srvtoolu_ prefixed call must be marked Hosted — it runs server-side, no local process to delegate to")
	}
}

func TestParseClaudeTurn_SetsVendorAndRaw(t *testing.T) {
	raw := []byte(`[{"type":"text","text":"hi"}]`)
	turn, err := ngl.ParseClaudeTurn(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn.Vendor != "claude" {
		t.Fatalf("got vendor %q", turn.Vendor)
	}
	if string(turn.Raw) != string(raw) {
		t.Fatalf("Raw was not preserved verbatim")
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Text != "hi" {
		t.Fatalf("got parts %+v", turn.Parts)
	}
}

// TestClaudeNotebookEditViews matches NotebookEdit's confirmed shape
// exactly — genuine before/after full-file content, unlike Write's
// null-originalFile convention, so this must populate Before/After, not
// WholeFile.
func TestClaudeNotebookEditViews(t *testing.T) {
	result := []byte(`{
		"new_source": "print('hi')",
		"cell_type": "code",
		"language": "python",
		"edit_mode": "insert",
		"cell_id": "cell-1",
		"notebook_path": "analysis.ipynb",
		"original_file": "{\"cells\": []}",
		"updated_file": "{\"cells\": [{\"source\": \"print('hi')\"}]}"
	}`)
	views, err := ngl.ClaudeNotebookEditViews(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if views.Path != "analysis.ipynb" {
		t.Fatalf("got path %q", views.Path)
	}
	if views.Before == "" || views.After == "" {
		t.Fatalf("expected genuine Before/After, got Before=%q After=%q", views.Before, views.After)
	}
	if views.WholeFile != nil {
		t.Fatalf("NotebookEdit must not populate WholeFile — it has a real pre-image, unlike Write")
	}
}

// TestClaudeToolStructs_MatchDocumentedFieldNames is a round-trip check
// that every struct's json tags match the exact field names captured live
// in agent_cli_interop.md §1 — catches a typo'd tag that would otherwise
// silently produce zero values instead of a decode error.
func TestClaudeToolStructs_MatchDocumentedFieldNames(t *testing.T) {
	var bash ngl.ClaudeBashResult
	mustUnmarshal(t, `{"stdout":"ok","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}`, &bash)
	if bash.Stdout != "ok" {
		t.Fatalf("got %+v", bash)
	}

	var glob ngl.ClaudeGlobResult
	mustUnmarshal(t, `{"filenames":["a.py","b.py"],"durationMs":12,"numFiles":2,"truncated":false,"totalMatches":2,"countIsComplete":true}`, &glob)
	if len(glob.Filenames) != 2 || !glob.CountIsComplete {
		t.Fatalf("got %+v", glob)
	}

	var wf ngl.ClaudeWebFetchResult
	mustUnmarshal(t, `{"bytes":1024,"code":200,"codeText":"OK","result":"summary text","durationMs":500,"url":"https://example.com"}`, &wf)
	if wf.Result != "summary text" {
		t.Fatalf("got %+v", wf)
	}

	var ws ngl.ClaudeWebSearchResult
	mustUnmarshal(t, `{"query":"golang generics","results":[{"tool_use_id":"srvtoolu_01x","content":[{"title":"Go Generics","url":"https://go.dev"}]}],"durationSeconds":1.2,"searchCount":1}`, &ws)
	if len(ws.Results) != 1 || ws.Results[0].IsSummary || ws.Results[0].Hit == nil || len(ws.Results[0].Hit.Content) != 1 || ws.Results[0].Hit.Content[0].Title != "Go Generics" {
		t.Fatalf("got %+v", ws)
	}

	var task ngl.ClaudeTaskResult
	mustUnmarshal(t, `{"summary":"done","totalTokens":500,"totalDurationMs":3000,"totalToolUseCount":4,"resolvedModel":"claude-sonnet-5","toolStats":{"readCount":2,"searchCount":1,"bashCount":0,"editFileCount":1,"linesAdded":10,"linesRemoved":2,"otherToolCount":0}}`, &task)
	if task.ToolStats.ReadCount != 2 || task.ToolStats.LinesAdded != 10 {
		t.Fatalf("got %+v", task)
	}

	var read ngl.ClaudeReadResult
	mustUnmarshal(t, `{"type":"notebook","file":{"filePath":"x.ipynb","cells":[]}}`, &read)
	if read.Type != "notebook" || read.NotebookFile == nil || read.NotebookFile.FilePath != "x.ipynb" {
		t.Fatalf("got %+v", read)
	}
}

// TestClaudeWebSearchResult_HeterogeneousResultsArray matches a REAL
// captured payload verbatim (claude_capture2.jsonl, 2026-07-26 live
// research pass): Results is a hit object followed by a synthesized
// summary STRING, in that order, not a homogeneous array of hit objects.
func TestClaudeWebSearchResult_HeterogeneousResultsArray(t *testing.T) {
	raw := []byte(`{
		"query": "python PEP 8 docstring conventions",
		"results": [
			{
				"tool_use_id": "srvtoolu_01Cyv1i6atKgGABVXmY2S4GM",
				"content": [
					{"title": "PEP 8: The Style Guide for Python Code", "url": "https://pep8.org/"},
					{"title": "PEP 257 - Docstring Conventions", "url": "https://peps.python.org/pep-0257/"}
				]
			},
			"## Python PEP 8 Docstring Conventions\n\nHere are the key docstring conventions..."
		],
		"durationSeconds": 6.401404199999999,
		"searchCount": 1
	}`)
	var ws ngl.ClaudeWebSearchResult
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ws.Results) != 2 {
		t.Fatalf("got %d results, want 2 (one hit object, one summary string)", len(ws.Results))
	}
	if ws.Results[0].IsSummary || ws.Results[0].Hit == nil || len(ws.Results[0].Hit.Content) != 2 {
		t.Fatalf("got results[0] %+v, want a non-summary hit object with 2 content entries", ws.Results[0])
	}
	if !ws.Results[1].IsSummary || ws.Results[1].Summary == "" {
		t.Fatalf("got results[1] %+v, want a summary string", ws.Results[1])
	}
	if ws.DurationSeconds < 6.4 || ws.SearchCount != 1 {
		t.Fatalf("got DurationSeconds=%v SearchCount=%v, want both populated — they are real, confirmed fields", ws.DurationSeconds, ws.SearchCount)
	}
}

// TestExtractToolUseResult_WebSearchSiblingField matches the confirmed
// outer envelope shape of a stream-json "user" line carrying a tool
// result: the rich data lives in a top-level tool_use_result field
// SIBLING to "message", not inside message.content[].content (which only
// carries a flattened textual echo).
func TestExtractToolUseResult_WebSearchSiblingField(t *testing.T) {
	raw := []byte(`{
		"type": "user",
		"message": {
			"role": "user",
			"content": [{"type":"tool_result","tool_use_id":"toolu_01BMNzSDSSoro6gMJqoJ6Ssm","content":"Web search results for query: ..."}]
		},
		"session_id": "sess1",
		"uuid": "u1",
		"timestamp": "2026-07-26T00:00:00Z",
		"tool_use_result": {"query":"golang generics","results":[],"durationSeconds":1.0,"searchCount":1}
	}`)
	id, resultJSON, denied, ok, err := ngl.ExtractToolUseResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if id != "toolu_01BMNzSDSSoro6gMJqoJ6Ssm" {
		t.Fatalf("got tool_use_id %q", id)
	}
	if denied != "" {
		t.Fatalf("got unexpected denial text %q", denied)
	}
	var ws ngl.ClaudeWebSearchResult
	if err := json.Unmarshal(resultJSON, &ws); err != nil {
		t.Fatalf("resultJSON was not the tool_use_result bytes: %v", err)
	}
	if ws.Query != "golang generics" {
		t.Fatalf("got %+v", ws)
	}
}

// TestExtractToolUseResult_PermissionDenialIsBareString matches a REAL
// confirmed shape: on a permission denial, tool_use_result is a bare JSON
// string, not an object — genuine type heterogeneity at the sibling-field
// level itself, not just within one tool's Results array.
func TestExtractToolUseResult_PermissionDenialIsBareString(t *testing.T) {
	raw := []byte(`{
		"type": "user",
		"message": {
			"role": "user",
			"content": [{"type":"tool_result","content":"Claude requested permissions to use WebSearch, but you haven't granted it yet.","is_error":true,"tool_use_id":"toolu_01ABaf4BScmBc9nGEErFcCbs"}]
		},
		"tool_use_result": "Error: Claude requested permissions to use WebSearch, but you haven't granted it yet."
	}`)
	id, resultJSON, denied, ok, err := ngl.ExtractToolUseResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if id != "toolu_01ABaf4BScmBc9nGEErFcCbs" {
		t.Fatalf("got tool_use_id %q", id)
	}
	if resultJSON != nil {
		t.Fatalf("expected resultJSON to be nil for the denial case, got %s", resultJSON)
	}
	if denied == "" {
		t.Fatalf("expected the denial text to be populated")
	}
}

func TestExtractToolUseResult_NoToolUseResultField(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	_, _, _, ok, err := ngl.ExtractToolUseResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("an ordinary chat turn with no tool_use_result must report ok=false")
	}
}

// TestClaudeToolSearchResult_DeferredToolLookup matches the confirmed live
// shape of ToolSearch's own lazy-load lookup result — this codebase's
// ToolSearch tool, observed from the other side of the wire.
func TestClaudeToolSearchResult_DeferredToolLookup(t *testing.T) {
	raw := []byte(`{"matches":["WebSearch"],"query":"select:WebSearch","total_deferred_tools":20}`)
	var r ngl.ClaudeToolSearchResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Matches) != 1 || r.Matches[0] != "WebSearch" || r.TotalDeferredTools != 20 {
		t.Fatalf("got %+v", r)
	}
}

// TestClaudeResultEvent_PermissionDenials matches the confirmed live shape
// of the terminal "result" event when a run hit a denied tool call
// (permission-behavior research pass, 2026-07-26).
func TestClaudeResultEvent_PermissionDenials(t *testing.T) {
	raw := []byte(`{
		"type": "result",
		"subtype": "success",
		"is_error": false,
		"result": "I wasn't able to delete that file — permission denied.",
		"session_id": "sess1",
		"permission_denials": [
			{"tool_name": "Bash", "tool_use_id": "toolu_1", "tool_input": {"command": "rm old_auth.py"}}
		]
	}`)
	var ev ngl.ClaudeResultEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ev.PermissionDenials) != 1 {
		t.Fatalf("got %d denials, want 1", len(ev.PermissionDenials))
	}
	if ev.PermissionDenials[0].ToolName != "Bash" {
		t.Fatalf("got %+v", ev.PermissionDenials[0])
	}
}

func TestClaudeResultEvent_NoDenialsWhenClean(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess1"}`)
	var ev ngl.ClaudeResultEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ev.PermissionDenials) != 0 {
		t.Fatalf("got %+v, want no denials", ev.PermissionDenials)
	}
}

func mustUnmarshal(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
}

// TestClaudeEditViews_HunksConvertToUnifiedText proves the converter
// registry actually works end to end for a real Claude-shaped edit, not
// just in isolation.
func TestClaudeEditViews_HunksConvertToUnifiedText(t *testing.T) {
	result := []byte(`{
		"filePath": "x.py",
		"originalFile": "old\n",
		"structuredPatch": [
			{"oldStart": 1, "oldLines": 1, "newStart": 1, "newLines": 1, "lines": ["-old", "+new"]}
		]
	}`)
	views, err := ngl.ClaudeEditViews(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := views.Get("unified_text")
	if !ok {
		t.Fatalf("expected unified_text to be derivable from hunks")
	}
	text := val.(string)
	if text == "" {
		t.Fatalf("got empty unified text")
	}
}
