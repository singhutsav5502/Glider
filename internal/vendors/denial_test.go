package vendors_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

// TestDetectDenials_CursorAgent matches a REAL captured shape (permission-
// behavior research pass, 2026-07-26, run without --force): a rejected
// shellToolCall inline in the stream-json stdout, mixed in with other event
// types that must be silently skipped, not misread as denials.
func TestDetectDenials_CursorAgent(t *testing.T) {
	stdout := []byte(`{"type":"system","subtype":"init","session_id":"s1"}
{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"shellToolCall":{"args":{"command":"rm old_auth.py"},"result":{"rejected":{"command":"rm old_auth.py","reason":""}},"toolCallId":"c1"}}}
{"type":"tool_call","subtype":"completed","call_id":"c2","tool_call":{"readToolCall":{"args":{"path":"x.py"},"result":{"success":{"content":"ok"}},"toolCallId":"c2"}}}
{"type":"result","subtype":"success","is_error":false,"result":"blocked","session_id":"s1"}`)

	denials, err := vendors.DetectDenials("cursor-agent", stdout, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(denials) != 1 {
		t.Fatalf("got %d denials, want 1 (the success and non-tool_call lines must not be misread)", len(denials))
	}
	if denials[0].ToolName != "shellToolCall" || denials[0].Detail != "rm old_auth.py" {
		t.Fatalf("got %+v", denials[0])
	}
}

// TestDetectDenials_Claude matches the confirmed terminal result-event
// shape carrying permission_denials.
func TestDetectDenials_Claude(t *testing.T) {
	stdout := []byte(`{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it"}]},"session_id":"s1"}
{"type":"result","subtype":"success","is_error":false,"result":"blocked","session_id":"s1","permission_denials":[{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"rm old_auth.py"}}]}`)

	denials, err := vendors.DetectDenials("claude", stdout, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(denials) != 1 || denials[0].ToolName != "Bash" {
		t.Fatalf("got %+v", denials)
	}
}

// TestDetectDenials_Agy matches agy's confirmed stderr denial message
// verbatim (permission-behavior research pass, 2026-07-26) — the only
// signal available for this vendor since it has no --output-format flag.
func TestDetectDenials_Agy(t *testing.T) {
	stderr := []byte(`jetski: no output produced — a tool required the "command" permission that headless mode cannot prompt for, so it was auto-denied. Add an allow-rule under permissions.allow in settings.json (e.g. command(<target>)). Alternatively, re-run with --dangerously-skip-permissions to auto-approve all tools.`)

	denials, err := vendors.DetectDenials("agy", nil, stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(denials) != 1 || denials[0].ToolName != "command" {
		t.Fatalf("got %+v", denials)
	}
}

func TestDetectDenials_AgyCleanRunHasNoDenials(t *testing.T) {
	denials, err := vendors.DetectDenials("agy", nil, []byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(denials) != 0 {
		t.Fatalf("got %+v, want none", denials)
	}
}

func TestDetectDenials_UnknownVendorReturnsNilNotError(t *testing.T) {
	denials, err := vendors.DetectDenials("some-future-vendor", []byte("anything"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denials != nil {
		t.Fatalf("got %+v, want nil for a vendor with no detector wired up", denials)
	}
}

// TestExtractEditViews_CursorAgent uses a REAL captured editToolCall
// completion (2026-07-26 live delegation test, cursor-agent adding a
// subtract() function to calc.py) — genuine NGL wire-format parsing, not a
// fabricated fixture.
func TestExtractEditViews_CursorAgent(t *testing.T) {
	stdout := []byte(`{"type":"system","subtype":"init","session_id":"s1"}
{"type":"tool_call","subtype":"started","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"calc.py"},"toolCallId":"c1"}}}
{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"calc.py"},"result":{"success":{"content":"def add(a, b):\n    return a + b\n"}},"toolCallId":"c1"}}}
{"type":"tool_call","subtype":"completed","call_id":"c2","tool_call":{"editToolCall":{"args":{"path":"calc.py","streamContent":"def add(a, b):\n    return a + b\n\n\ndef subtract(a, b):\n    return a - b\n"},"result":{"success":{"path":"calc.py","linesAdded":4,"linesRemoved":0,"diffString":"--- a/calc.py\n+++ b/calc.py\n@@ -1,2 +1,6 @@\n def add(a, b):\n     return a + b\n+\n+\n+def subtract(a, b):\n+    return a - b","beforeFullFileContent":"def add(a, b):\n    return a + b\n","afterFullFileContent":"def add(a, b):\n    return a + b\n\n\ndef subtract(a, b):\n    return a - b\n","message":"The file calc.py has been updated."}},"toolCallId":"c2"}}}
{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`)

	a := vendors.AdapterForTest("cursor-agent")
	views, ok := a.ExtractEditViews(stdout)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if views.Path != "calc.py" {
		t.Fatalf("got path %q", views.Path)
	}
	unified, ok := views.Get("unified_text")
	if !ok {
		t.Fatalf("expected a unified_text view to be directly available")
	}
	diff, _ := unified.(string)
	if diff == "" || diff != views.UnifiedText {
		t.Fatalf("got diff %q", diff)
	}
	if !strings.Contains(diff, "+def subtract(a, b):") {
		t.Fatalf("got diff %q, missing the expected addition", diff)
	}
}

// TestExtractEditViews_CursorAgent_NoEditReturnsFalse proves a run with no
// edit (e.g. a pure read/inspection) doesn't fabricate a diff.
func TestExtractEditViews_CursorAgent_NoEditReturnsFalse(t *testing.T) {
	stdout := []byte(`{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"x.py"},"result":{"success":{"content":"hi"}},"toolCallId":"c1"}}}`)
	a := vendors.AdapterForTest("cursor-agent")
	if _, ok := a.ExtractEditViews(stdout); ok {
		t.Fatalf("expected ok=false for a run with no edit")
	}
}

// TestExtractEditViews_Claude covers the two-pass correlation this
// adapter needs: the tool NAME only appears on the "assistant" event that
// requested it, the actual result only appears later on a "user" event
// correlated by tool_use_id via the tool_use_result sibling-field pattern.
func TestExtractEditViews_Claude(t *testing.T) {
	stdout := []byte(`{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{"file_path":"calc.py"}}]},"session_id":"s1"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Applied edit"}]},"session_id":"s1","tool_use_result":{"filePath":"calc.py","originalFile":"def add(a, b):\n    return a + b\n","structuredPatch":[{"oldStart":1,"oldLines":2,"newStart":1,"newLines":6,"lines":[" def add(a, b):","     return a + b","+","+","+def subtract(a, b):","+    return a - b"]}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`)

	a := vendors.AdapterForTest("claude")
	views, ok := a.ExtractEditViews(stdout)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if views.Path != "calc.py" || len(views.Hunks) != 1 {
		t.Fatalf("got %+v", views)
	}
	unified, ok := views.Get("unified_text")
	if !ok {
		t.Fatalf("expected unified_text derivable via the hunks->unified_text converter")
	}
	if !strings.Contains(unified.(string), "+def subtract(a, b):") {
		t.Fatalf("got %q", unified)
	}
}

// TestExtractEditViews_Claude_NonEditToolIgnored proves a tool_use_result
// belonging to a non-edit tool (e.g. Bash) isn't misread as an edit.
func TestExtractEditViews_Claude_NonEditToolIgnored(t *testing.T) {
	stdout := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},"session_id":"s1"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file1 file2"}]},"session_id":"s1","tool_use_result":{"stdout":"file1 file2","stderr":"","interrupted":false}}`)
	a := vendors.AdapterForTest("claude")
	if _, ok := a.ExtractEditViews(stdout); ok {
		t.Fatalf("expected ok=false for a non-edit tool result")
	}
}

// TestExtractEditViews_Agy_AlwaysFalse is the honesty check: agy's
// headless output has no structured diff to parse, ever — no attempt to
// fabricate one from prose.
func TestExtractEditViews_Agy_AlwaysFalse(t *testing.T) {
	a := vendors.AdapterForTest("agy")
	if _, ok := a.ExtractEditViews([]byte("I have added and inspected the directory...")); ok {
		t.Fatalf("expected ok=false — agy's headless output is prose-only")
	}
}

func TestFormatEditSummary_NilViewsReturnsEmpty(t *testing.T) {
	if got := vendors.FormatEditSummary("cursor-agent", nil); got != "" {
		t.Fatalf("got %q, want empty for nil views", got)
	}
}

func TestFormatEditSummary_IncludesDiffAndPath(t *testing.T) {
	a := vendors.AdapterForTest("cursor-agent")
	stdout := []byte(`{"type":"tool_call","subtype":"completed","call_id":"c2","tool_call":{"editToolCall":{"args":{"path":"calc.py"},"result":{"success":{"path":"calc.py","diffString":"--- a/calc.py\n+++ b/calc.py\n@@ -1 +1,2 @@\n def add(a, b):\n+def subtract(a, b):","beforeFullFileContent":"x","afterFullFileContent":"y"}},"toolCallId":"c2"}}}`)
	views, ok := a.ExtractEditViews(stdout)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	summary := vendors.FormatEditSummary("cursor-agent", &views)
	if !strings.Contains(summary, "calc.py") || !strings.Contains(summary, "cursor-agent") || !strings.Contains(summary, "def subtract") {
		t.Fatalf("got %q", summary)
	}
}

