package ngl_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// Shapes below match live captures taken 2026-07-26 by running real
// `cursor-agent -p ... --output-format stream-json` sessions — several
// corrections to planning/agent_cli_interop.md §2's original, narrower
// captures. See adapter_cursor.go's doc comments for what changed and why.

// TestParseCursorToolCall_PopulatesID is the regression test for the real
// bug this pass found: the earlier version of ParseCursorToolCall never
// populated ToolCall.ID at all, because it only ever looked at the inner
// tool_call.<name> object and never the outer envelope's call_id/toolCallId.
func TestParseCursorToolCall_PopulatesID(t *testing.T) {
	raw := []byte(`{
		"type": "tool_call",
		"subtype": "started",
		"call_id": "call-abc123-1\nfc_def456_1",
		"tool_call": {
			"readToolCall": {
				"args": {"path": "math_utils.py"},
				"toolCallId": "call-abc123-1\nfc_def456_1",
				"startedAtMs": "1234567890"
			}
		},
		"model_call_id": "mc_1",
		"session_id": "sess_1",
		"timestamp_ms": 1234567890
	}`)
	tc, err := ngl.ParseCursorToolCall(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tc.Name != "readToolCall" {
		t.Fatalf("got name %q, want readToolCall", tc.Name)
	}
	if tc.ID != "call-abc123-1\nfc_def456_1" {
		t.Fatalf("got ID %q, want the toolCallId populated — this is the bug this test guards against", tc.ID)
	}
}

func TestParseCursorToolCall_FallsBackToCallIDWhenToolCallIDMissing(t *testing.T) {
	raw := []byte(`{"type":"tool_call","call_id":"call-xyz-1","tool_call":{"editToolCall":{"args":{"path":"x.py"}}}}`)
	tc, err := ngl.ParseCursorToolCall(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tc.ID != "call-xyz-1" {
		t.Fatalf("got ID %q, want fallback to the envelope's call_id", tc.ID)
	}
}

// TestCursorToolResult_CompletedExtractsSuccessPayload matches the real
// completed-event shape: result.success nested one level inside the
// tool_call.<name> object, sibling to args.
func TestCursorToolResult_CompletedExtractsSuccessPayload(t *testing.T) {
	raw := []byte(`{
		"type": "tool_call",
		"subtype": "completed",
		"call_id": "call-1",
		"tool_call": {
			"readToolCall": {
				"args": {"path": "x.py"},
				"result": {"success": {"content":"print(1)\n"}},
				"toolCallId": "call-1",
				"completedAtMs": "1234567999"
			}
		}
	}`)
	name, result, ok, err := ngl.CursorToolResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for a completed event with result.success")
	}
	if name != "readToolCall" {
		t.Fatalf("got name %q", name)
	}
	if string(result) != `{"content":"print(1)\n"}` {
		t.Fatalf("got result %s", result)
	}
}

// TestCursorToolRejection_MatchesRealShape matches a REAL captured payload
// (permission-behavior research pass, 2026-07-26, run without --force):
// a completed shellToolCall whose result is "rejected", not "success".
func TestCursorToolRejection_MatchesRealShape(t *testing.T) {
	raw := []byte(`{
		"type": "tool_call",
		"subtype": "completed",
		"call_id": "call-1",
		"tool_call": {
			"shellToolCall": {
				"args": {"command": "rm old_auth.py"},
				"result": {"rejected": {"command": "rm old_auth.py", "reason": ""}},
				"toolCallId": "call-1"
			}
		}
	}`)
	name, rejection, ok, err := ngl.CursorToolRejection(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for a rejected result")
	}
	if name != "shellToolCall" {
		t.Fatalf("got name %q", name)
	}
	if rejection.Command != "rm old_auth.py" {
		t.Fatalf("got rejection %+v", rejection)
	}
}

func TestCursorToolRejection_SuccessIsNotARejection(t *testing.T) {
	raw := []byte(`{"type":"tool_call","subtype":"completed","call_id":"call-1","tool_call":{"readToolCall":{"args":{},"result":{"success":{"content":"x"}},"toolCallId":"call-1"}}}`)
	_, _, ok, err := ngl.CursorToolRejection(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("a successful result must not be reported as a rejection")
	}
}

func TestCursorToolResult_StartedHasNoResultYet(t *testing.T) {
	raw := []byte(`{"type":"tool_call","subtype":"started","call_id":"call-1","tool_call":{"readToolCall":{"args":{"path":"x.py"}}}}`)
	_, _, ok, err := ngl.CursorToolResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("a started event must not claim to have a result yet")
	}
}

// TestCursorEditViews_PopulatesUnifiedTextAndBeforeAfter matches
// editToolCall's confirmed result shape exactly: diffString plus both full
// before/after snapshots, richer than Claude's hunks-only shape.
func TestCursorEditViews_PopulatesUnifiedTextAndBeforeAfter(t *testing.T) {
	result := []byte(`{
		"diffString": "--- a/math_utils.py\n+++ b/math_utils.py\n@@ -1,2 +1,2 @@\n-def add(a, b):\n+def add(a, b, c=0):\n",
		"beforeFullFileContent": "def add(a, b):\n    return a + b\n",
		"afterFullFileContent": "def add(a, b, c=0):\n    return a + b + c\n",
		"linesAdded": 1,
		"linesRemoved": 1
	}`)
	views, err := ngl.CursorEditViews("math_utils.py", result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if views.UnifiedText == "" {
		t.Fatalf("expected UnifiedText to be populated natively")
	}
	if views.Before == "" || views.After == "" {
		t.Fatalf("expected Before and After to be populated natively (richer than Claude's shape)")
	}
	if views.Hunks != nil {
		t.Fatalf("cursor-agent's native shape has no hunks — must not be fabricated")
	}
	// Both directly available (not derived) — confirms this vendor's shape
	// needs no converter to answer either question, unlike Claude's.
	if _, ok := views.Get("unified_text"); !ok {
		t.Fatalf("expected unified_text directly available")
	}
	if _, ok := views.Get("before_after"); !ok {
		t.Fatalf("expected before_after directly available")
	}
}

// TestCursorEditToolCallArgs_StreamContentNotOldNew matches the corrected
// args shape: {path, streamContent} — a proposed full-file replacement,
// not an {old,new} snippet pair. cursor-agent computes the diff
// server-side (see CursorEditViews' result-side diffString), so the args
// alone carry no diff.
func TestCursorEditToolCallArgs_StreamContentNotOldNew(t *testing.T) {
	raw := []byte(`{"path":"x.py","streamContent":"def add(a, b, c=0):\n    return a + b + c\n"}`)
	var args ngl.CursorEditToolCallArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Path != "x.py" || args.StreamContent == "" {
		t.Fatalf("got %+v", args)
	}
}

func TestParseCursorAgentTurn_ToolCallSetsVendorAndPart(t *testing.T) {
	raw := []byte(`{"type":"tool_call","call_id":"call-abc123-1\nfc_def456_1","tool_call":{"readToolCall":{"args":{"path":"x.py"},"toolCallId":"call-abc123-1\nfc_def456_1"}}}`)
	turn, err := ngl.ParseCursorAgentTurn(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn.Vendor != "cursor-agent" {
		t.Fatalf("got vendor %q", turn.Vendor)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Kind != ngl.PartToolCall {
		t.Fatalf("got parts %+v", turn.Parts)
	}
	if turn.Parts[0].ToolCall.ID != "call-abc123-1\nfc_def456_1" {
		t.Fatalf("expected the fixed ID population to flow through Turn assembly, got %q", turn.Parts[0].ToolCall.ID)
	}
}

// TestParseCursorAgentTurn_UserAndAssistantTextEvents covers the two event
// types the original narrower pass never modeled at all: "user" and
// "assistant" stream-json lines, Anthropic-block-shaped.
func TestParseCursorAgentTurn_UserAndAssistantTextEvents(t *testing.T) {
	userRaw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"add a test"}]},"session_id":"s1"}`)
	turn, err := ngl.ParseCursorAgentTurn(userRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn.Vendor != "cursor-agent" || len(turn.Parts) != 1 || turn.Parts[0].Text != "add a test" {
		t.Fatalf("got turn %+v", turn)
	}

	assistantRaw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"s1"}`)
	turn, err = ngl.ParseCursorAgentTurn(assistantRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Text != "done" {
		t.Fatalf("got turn %+v", turn)
	}
}

// TestParseCursorAgentTurn_ThinkingEvent covers the reasoning-stream event
// type: Text present for "delta", absent for the closing "completed" marker.
func TestParseCursorAgentTurn_ThinkingEvent(t *testing.T) {
	delta := []byte(`{"type":"thinking","subtype":"delta","text":"considering approach...","session_id":"s1","timestamp_ms":1}`)
	turn, err := ngl.ParseCursorAgentTurn(delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].Kind != ngl.PartReasoning || turn.Parts[0].Text != "considering approach..." {
		t.Fatalf("got turn %+v", turn)
	}

	completed := []byte(`{"type":"thinking","subtype":"completed","session_id":"s1","timestamp_ms":2}`)
	turn, err = ngl.ParseCursorAgentTurn(completed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turn.Parts) != 0 {
		t.Fatalf("a completed marker carries no text of its own, expected no Parts, got %+v", turn.Parts)
	}
}

// TestParseCursorAgentTurn_SystemInitAndResultCarryNoParts covers the two
// event types that are session/protocol bookkeeping, not turn content —
// Raw must be preserved, but Parts must stay empty rather than force
// something into it.
func TestParseCursorAgentTurn_SystemInitAndResultCarryNoParts(t *testing.T) {
	initRaw := []byte(`{"type":"system","subtype":"init","apiKeySource":"env","cwd":"/repo","session_id":"s1","model":"gpt-5","permissionMode":"default"}`)
	turn, err := ngl.ParseCursorAgentTurn(initRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turn.Parts) != 0 || turn.Vendor != "cursor-agent" {
		t.Fatalf("got turn %+v", turn)
	}

	resultRaw := []byte(`{"type":"result","subtype":"success","duration_ms":100,"duration_api_ms":80,"is_error":false,"result":"done","session_id":"s1","request_id":"r1","usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}`)
	turn, err = ngl.ParseCursorAgentTurn(resultRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(turn.Parts) != 0 {
		t.Fatalf("got turn %+v", turn)
	}
}

// TestCursorInteractionQuery_WebSearchApprovalGate covers the genuinely new
// event type this pass found: a permission-gate protocol distinct from the
// tool_call lifecycle, observed wrapping a web search request/response.
func TestCursorInteractionQuery_WebSearchApprovalGate(t *testing.T) {
	raw := []byte(`{
		"type": "interaction_query",
		"subtype": "response",
		"query_type": "webSearchRequestQuery",
		"response": {"id": 0, "webSearchRequestResponse": {"approved": {}}},
		"session_id": "s1"
	}`)
	var q ngl.CursorInteractionQuery
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.QueryType != "webSearchRequestQuery" || q.Subtype != "response" {
		t.Fatalf("got %+v", q)
	}
	if len(q.Response) == 0 {
		t.Fatalf("expected Response to carry the raw approval payload")
	}
}

// TestParseCursorCompositeID matches the documented composite id shape
// exactly: "call-<uuid>-<n>\nfc_<uuid>_<n>" — a Cursor wrapper id
// concatenated via embedded newline with an OpenAI-style fc_… id.
func TestParseCursorCompositeID(t *testing.T) {
	id, ok := ngl.ParseCursorCompositeID("call-abc123-1\nfc_def456_1")
	if !ok {
		t.Fatalf("expected a composite id match")
	}
	if id.Cursor != "call-abc123-1" || id.OpenAI != "fc_def456_1" {
		t.Fatalf("got %+v", id)
	}
}

func TestParseCursorCompositeID_PlainIDIsNotComposite(t *testing.T) {
	_, ok := ngl.ParseCursorCompositeID("toolu_plain_id")
	if ok {
		t.Fatalf("a plain id with no embedded newline must not be treated as composite")
	}
}

// TestCursorGlobResult_MatchesRealShape replaces an earlier, unverified
// guess ({filenames, durationMs, numFiles, truncated, totalMatches,
// countIsComplete} — none of those field names turned out to be real)
// with the shape actually captured live: pattern/path/files/totalFiles/
// clientTruncated/ripgrepTruncated.
func TestCursorGlobResult_MatchesRealShape(t *testing.T) {
	raw := []byte(`{
		"pattern": "**/*.py",
		"path": "/repo",
		"files": ["math_utils.py", "test_math_utils.py"],
		"totalFiles": 2,
		"clientTruncated": false,
		"ripgrepTruncated": false
	}`)
	var result ngl.CursorGlobResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalFiles != 2 || len(result.Files) != 2 {
		t.Fatalf("got %+v", result)
	}
}

func TestCursorGlobArgs_MatchesRealShape(t *testing.T) {
	raw := []byte(`{"globPattern":"**/*.py"}`)
	var args ngl.CursorGlobArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.GlobPattern != "**/*.py" {
		t.Fatalf("got %+v", args)
	}
}

// TestCursorGrepArgs_MatchesRealShape covers the args struct that didn't
// exist at all before this pass — real pagination and matching controls
// (offset, caseInsensitive, multiline) confirmed live.
func TestCursorGrepArgs_MatchesRealShape(t *testing.T) {
	raw := []byte(`{"pattern":"def add","glob":"*.py","caseInsensitive":false,"multiline":false,"toolCallId":"call-1","offset":0}`)
	var args ngl.CursorGrepArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.Pattern != "def add" || args.ToolCallID != "call-1" {
		t.Fatalf("got %+v", args)
	}
}

// TestCursorGrepResult_NestedMatches replaces an earlier, one-level-too-
// shallow guess (workspaceResults keyed directly by file path) with the
// real two-level shape confirmed live: workspaceResults is keyed by
// WORKSPACE ROOT, and each root's content.matches is an array of per-FILE
// match groups, each carrying its own nested matches[].
func TestCursorGrepResult_NestedMatches(t *testing.T) {
	raw := []byte(`{
		"pattern": "def add",
		"path": "/repo",
		"outputMode": "content",
		"workspaceResults": {
			"/repo": {
				"content": {
					"matches": [
						{
							"file": "math_utils.py",
							"matches": [
								{"lineNumber": 3, "content": "def add(a, b):", "contentTruncated": false, "isContextLine": false},
								{"lineNumber": 4, "content": "    return a + b", "contentTruncated": false, "isContextLine": true}
							]
						}
					],
					"totalLines": 2,
					"totalMatchedLines": 1,
					"clientTruncated": false,
					"ripgrepTruncated": false
				}
			}
		}
	}`)
	var result ngl.CursorGrepResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root, ok := result.WorkspaceResults["/repo"]
	if !ok {
		t.Fatalf("expected /repo in workspaceResults")
	}
	if len(root.Content.Matches) != 1 {
		t.Fatalf("got %d file-match groups, want 1", len(root.Content.Matches))
	}
	fileMatches := root.Content.Matches[0]
	if fileMatches.File != "math_utils.py" {
		t.Fatalf("got file %q", fileMatches.File)
	}
	if len(fileMatches.Matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(fileMatches.Matches))
	}
	if fileMatches.Matches[0].LineNumber != 3 || fileMatches.Matches[0].IsContextLine {
		t.Fatalf("got %+v", fileMatches.Matches[0])
	}
	if root.Content.TotalMatchedLines != 1 {
		t.Fatalf("got TotalMatchedLines %d", root.Content.TotalMatchedLines)
	}
}

// TestCursorUpdateTodos_DependenciesDAG matches updateTodosToolCall's
// confirmed shape — a real DAG-between-todos field not previously
// documented for any of the three CLIs.
func TestCursorUpdateTodos_DependenciesDAG(t *testing.T) {
	raw := []byte(`{
		"todos": [
			{"id": "t1", "content": "write tests", "status": "TODO_STATUS_PENDING", "createdAt": "2026-07-26T00:00:00Z", "updatedAt": "2026-07-26T00:00:00Z", "dependencies": ["t0"]}
		],
		"merge": true
	}`)
	var args ngl.CursorUpdateTodosArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args.Todos) != 1 {
		t.Fatalf("got %d todos", len(args.Todos))
	}
	if len(args.Todos[0].Dependencies) != 1 || args.Todos[0].Dependencies[0] != "t0" {
		t.Fatalf("got dependencies %+v", args.Todos[0].Dependencies)
	}
	if !args.Merge {
		t.Fatalf("expected Merge to be true")
	}
}

func TestCursorWebFetchAndWebSearch_MatchDocumentedShapes(t *testing.T) {
	var fetchResult ngl.CursorWebFetchResult
	if err := json.Unmarshal([]byte(`{"url":"https://example.com","markdown":"# Example\n\nContent here."}`), &fetchResult); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchResult.Markdown == "" {
		t.Fatalf("expected markdown to be populated (cursor-agent pre-converts, unlike Claude's model-summarized WebFetch)")
	}

	// Real live capture: references typically holds ONE entry — a
	// synthesized chunk (Links/Synthesis/Highlights), not one reference per
	// search hit as the field names alone might suggest.
	var searchResult ngl.CursorWebSearchResult
	if err := json.Unmarshal([]byte(`{"references":[{"title":"Web search results","url":"","chunk":"Links:\n- https://go.dev\n\nSynthesis:\nGo's stdlib..."}]}`), &searchResult); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(searchResult.References) != 1 || searchResult.References[0].Title != "Web search results" {
		t.Fatalf("got %+v", searchResult.References)
	}
}

// TestParseCursorDeleteResult_PreservesContent matches deleteToolCall's
// result.success shape, and the specific finding worth generalizing: the
// deleted file's content is preserved, not discarded.
func TestParseCursorDeleteResult_PreservesContent(t *testing.T) {
	result := []byte(`{"path":"old_module.py","deletedFile":true,"fileSize":"142","prevContent":"# old code\n"}`)
	tr, err := ngl.ParseCursorDeleteResult(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.Text != "# old code\n" {
		t.Fatalf("got %q, want the preserved deleted content", tr.Text)
	}
}
