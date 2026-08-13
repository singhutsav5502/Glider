package ngl_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

func TestResolveDelegateRenderer_KnownAndUnknownVendors(t *testing.T) {
	for _, name := range []string{"claude", "cursor-agent", "agy"} {
		if r := ngl.ResolveDelegateRenderer(name); r == nil || r.Vendor() != name {
			t.Fatalf("expected a registered renderer for %q, got %v", name, r)
		}
	}
	if r := ngl.ResolveDelegateRenderer("some-future-vendor"); r != nil {
		t.Fatalf("expected nil for an unregistered vendor, got %v", r)
	}
}

// claudeStreamJSONFixture is a realistic multi-line NDJSON transcript
// shaped like real Claude Code headless output (planning/agent_cli_interop.md's
// confirmed shape) — system/init, one assistant text delta, a terminal
// result line. The renderer's job is finding just the last line and
// pulling its "result" field.
const claudeStreamJSONFixture = `{"type":"system","subtype":"init","session_id":"sess1","model":"claude-x"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me look at that."}]},"session_id":"sess1"}
{"type":"result","subtype":"success","is_error":false,"result":"The bug was a missing null check in parseConfig.","session_id":"sess1"}
`

func TestClaudeDelegateRenderer_ExtractsTerminalResult(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("claude")
	clean, ok := r.Render([]byte(claudeStreamJSONFixture))
	if !ok {
		t.Fatal("expected ok=true for a well-formed transcript with a terminal result line")
	}
	if clean != "The bug was a missing null check in parseConfig." {
		t.Fatalf("got %q", clean)
	}
}

func TestCursorDelegateRenderer_ExtractsTerminalResult(t *testing.T) {
	// cursor-agent's own field names, confirmed identical to Claude's for
	// this purpose (adapter_cursor.go's CursorResultEvent).
	fixture := `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"thinking..."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Refactored extractUser into its own file.","session_id":"s1"}
`
	r := ngl.ResolveDelegateRenderer("cursor-agent")
	clean, ok := r.Render([]byte(fixture))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if clean != "Refactored extractUser into its own file." {
		t.Fatalf("got %q", clean)
	}
}

func TestClaudeDelegateRenderer_NoResultLineFallsBack(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("claude")
	// A truncated/streaming-interrupted transcript with no terminal result
	// event at all — must refuse, not guess from the transcript.
	fixture := `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"partial..."}]}}
`
	if _, ok := r.Render([]byte(fixture)); ok {
		t.Fatal("expected ok=false with no terminal result event")
	}
}

func TestClaudeDelegateRenderer_EmptyResultFallsBack(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("claude")
	fixture := `{"type":"result","subtype":"error","is_error":true,"result":"","session_id":"s1"}`
	if _, ok := r.Render([]byte(fixture)); ok {
		t.Fatal("expected ok=false for an empty result field — nothing clean to show")
	}
}

func TestClaudeDelegateRenderer_UsesLastResultLineIfMultiple(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("claude")
	fixture := `{"type":"result","result":"first (stale)","session_id":"s1"}
{"type":"result","result":"second (final)","session_id":"s1"}
`
	clean, ok := r.Render([]byte(fixture))
	if !ok || clean != "second (final)" {
		t.Fatalf("got clean=%q ok=%v, want the LAST result line", clean, ok)
	}
}

func TestAgyDelegateRenderer_PassesThroughPlainProse(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("agy")
	raw := "  I fixed the bug by adding a nil check.  \n"
	clean, ok := r.Render([]byte(raw))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if clean != "I fixed the bug by adding a nil check." {
		t.Fatalf("got %q", clean)
	}
	if strings.TrimSpace(clean) != clean {
		t.Fatalf("expected trimmed output")
	}
}

func TestAgyDelegateRenderer_EmptyFallsBack(t *testing.T) {
	r := ngl.ResolveDelegateRenderer("agy")
	if _, ok := r.Render([]byte("   ")); ok {
		t.Fatal("expected ok=false for whitespace-only output")
	}
}
