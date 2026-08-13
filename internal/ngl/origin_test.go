package ngl_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// instantReplyChan puts a reply text that the code already has into the
// <-chan string shape that OriginAdapter.WriteReply needs. That signature
// changed on 2026-07-29, as one part of the correction that keeps a streaming
// reply alive.
//
// Each WriteReply test in this package that does not examine the asynchronous
// path uses this function.
func instantReplyChan(text string) <-chan string {
	ch := make(chan string, 1)
	ch <- text
	close(ch)
	return ch
}

// TestResolveOriginAdapter_ClaudeAndAgyAreDistinctFronts is the regression
// test for a true condition that a live test showed on 2026-07-27.
//
// One request must agree with the front-CLI shape of exactly one registered
// vendor. And the dispatcher in mitm must never need to know which vendor that
// is.
//
// This test confirms the correction of the first defect. A fixed test on
// `/v1/messages` meant that the code recognized only the traffic of Claude Code
// as a front CLI. Therefore a delegate flag that a person typed in agy or in
// cursor-agent did nothing, and it gave no message.
func TestResolveOriginAdapter_ClaudeAndAgyAreDistinctFronts(t *testing.T) {
	claudeReq := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	a := ngl.ResolveOriginAdapter(claudeReq)
	if a == nil || a.Vendor() != "claude" {
		t.Fatalf("expected claude adapter for /v1/messages, got %v", a)
	}

	agyReq := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", nil)
	b := ngl.ResolveOriginAdapter(agyReq)
	if b == nil || b.Vendor() != "agy" {
		t.Fatalf("expected agy adapter for streamGenerateContent, got %v", b)
	}

	unknownReq := httptest.NewRequest(http.MethodPost, "https://example.com/whatever", nil)
	if got := ngl.ResolveOriginAdapter(unknownReq); got != nil {
		t.Fatalf("expected nil adapter for an unrecognized request, got %v", got)
	}
}

// TestResolveOriginAdapter_AgyMatchesWithPortInHost is agy's side of the
// same real bug internal/ngl/adapter_cursor_origin_test.go's
// _HostIncludesPort test guards for cursor-agent — Matches() must strip a
// ":port" suffix before comparing r.Host, not just work when it happens to
// be absent.
func TestResolveOriginAdapter_AgyMatchesWithPortInHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", nil)
	req.Host = "daily-cloudcode-pa.googleapis.com:443"
	a := ngl.ResolveOriginAdapter(req)
	if a == nil || a.Vendor() != "agy" {
		t.Fatalf("expected agy adapter to match even with an explicit :443 in Host, got %v", a)
	}
}

// agyStreamGenerateContentBody is the real captured shape (2026-07-27, via
// tools/wirecapture against agy's own live traffic), trimmed to the fields
// ExtractUserInstruction actually reads.
func agyStreamGenerateContentBody(userText, model string) string {
	return `{"project":"fiery-gearbox-3fcgj","requestId":"agent/x/1/y/2",` +
		`"request":{"contents":[{"role":"user","parts":[{"text":"<USER_REQUEST>\n` + userText +
		`\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-07-27T18:20:13+05:30.\n</ADDITIONAL_METADATA>"}]}]},` +
		`"model":"` + model + `","userAgent":"antigravity","requestType":"agent"}`
}

func TestAgyOriginAdapter_ExtractUserInstruction_StripsUserRequestWrapper(t *testing.T) {
	agyReq := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", nil)
	adapter := ngl.ResolveOriginAdapter(agyReq)
	if adapter == nil {
		t.Fatal("expected agy adapter to be resolved")
	}

	body := []byte(agyStreamGenerateContentBody("reply with exactly: WIRECAPTURE_OK", "gemini-3.6-flash-high"))
	text, model, stream, ok, err := adapter.ExtractUserInstruction(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a real captured-shape body")
	}
	if text != "reply with exactly: WIRECAPTURE_OK" {
		t.Fatalf("got text %q, want the unwrapped human instruction (agy's own <ADDITIONAL_METADATA> scaffold must not leak in)", text)
	}
	if model != "gemini-3.6-flash-high" {
		t.Fatalf("got model %q", model)
	}
	if !stream {
		t.Fatal("expected stream=true — agy's streamGenerateContent endpoint is inherently streaming")
	}
}

func TestAgyOriginAdapter_ExtractUserInstruction_RefusesUnknownShape(t *testing.T) {
	agyReq := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", nil)
	adapter := ngl.ResolveOriginAdapter(agyReq)

	// A structurally valid user turn, but without agy's own confirmed
	// <USER_REQUEST> wrapper — must refuse (ok=false), not guess at the
	// raw text, per OriginAdapter.ExtractUserInstruction's own contract.
	body := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"/agy do something"}]}]}},"model":"gemini-3.6-flash-high"}`)
	_, _, _, ok, err := adapter.ExtractUserInstruction(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a body without the confirmed <USER_REQUEST> wrapper — must never guess")
	}
}

func TestAgyOriginAdapter_WriteReply_MatchesCapturedShape(t *testing.T) {
	adapter := ngl.ResolveOriginAdapter(httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", nil))
	rw := httptest.NewRecorder()
	if err := adapter.WriteReply(rw, "gemini-3.6-flash-high", true, "", instantReplyChan("hello from glider")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct := rw.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("got Content-Type %q, want text/event-stream (matches agy's own real response)", ct)
	}
	body := rw.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("expected an SSE data: line, got %q", body)
	}
	if !strings.HasSuffix(body, "\r\n\r\n") {
		t.Fatalf("expected \\r\\n\\r\\n event boundary (matches the real captured response byte-for-byte), got %q", body)
	}
	if !strings.Contains(body, `"text":"hello from glider"`) {
		t.Fatalf("expected reply text embedded in candidates[].content.parts[].text, got %q", body)
	}
}
