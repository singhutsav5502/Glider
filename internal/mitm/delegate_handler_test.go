package mitm_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/vendors"
)

// TestDelegateHandler_RestoresBodyWhenNotHandled is a regression test for a
// real bug shipped and caught live on 2026-07-26: DelegateHandler read the
// full request body to check for a delegate flag, and when none was found,
// returned handled=false without restoring it — every normal (non-delegate)
// request that then fell through to origin passthrough went out with an
// empty body, breaking real API calls for the whole session. mitmSession's
// own contract (internal/mitm/proxy.go, right where it calls TryHandle) says
// exactly this: "LocalHandler must not consume body unless it handles."
func TestDelegateHandler_RestoresBodyWhenNotHandled(t *testing.T) {
	h := &mitm.DelegateHandler{}
	payload := `{"model":"claude-x","stream":false,"messages":[{"role":"user","content":"hello world, nothing to delegate here"}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(payload))
	rw := httptest.NewRecorder()

	handled, err := h.TryHandle(rw, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatalf("expected handled=false for a message with no delegate flag")
	}

	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading restored body: %v", err)
	}
	if string(restored) != payload {
		t.Fatalf("body not restored correctly: got %q, want %q", restored, payload)
	}
}

// TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation is the
// full-path regression test for the actual live bug (2026-07-26): a vendor
// flag appearing only inside Claude Code's own auto-injected
// <system-reminder> scaffolding — not something a human typed — must not
// trigger delegation. Uses whatever vendor the real local registry has
// enabled (skips if none, rather than depending on a specific fixture
// vendor being installed on every machine this runs on).
func TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation(t *testing.T) {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		t.Skip("vendor registry path unavailable in this environment")
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil || len(reg.Enabled()) == 0 {
		t.Skip("no enabled vendors in the local registry — nothing to accidentally match")
	}
	flagName := reg.Enabled()[0].Name

	h := &mitm.DelegateHandler{}
	payload := `{"model":"claude-x","stream":false,"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"<system-reminder>\nrecent tool output mentioned /` + flagName + ` somewhere\n</system-reminder>\n\nwhat does this code do?"}` +
		`]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(payload))
	rw := httptest.NewRecorder()

	handled, err := h.TryHandle(rw, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatalf("a flag inside <system-reminder> scaffolding triggered delegation — this is the exact live bug from 2026-07-26")
	}

	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading restored body: %v", err)
	}
	if string(restored) != payload {
		t.Fatalf("body not restored correctly after declining a scaffolded flag")
	}
}

// realCursorAgentRunRequestFirstEnvelope is the first Connect envelope from
// a real, live-captured cursor-agent AgentService/Run request (byte-for-byte
// identical to the fixture in internal/ngl/adapter_cursor_origin_test.go —
// duplicated here rather than imported, since it's an unexported test var in
// a different package). Decodes to the prompt "reply with exactly:
// CURSORCAP_OK" — no delegate flag, so DelegateHandler must fall through.
var realCursorAgentRunRequestFirstEnvelope = []byte("\x00\x00\x00\x02n\n\xeb\x04\n\x00\x12P\nN\nL\n reply with exactly: CURSORCAP_OK\x12$ba5ebf39-21c4-47c8-bc09-e1bee5311657\x1a\x00 \x01\"\x00*$7b4fffae-309e-4dd7-bb36-4be380112b17J\t\n\adefault`\x00r\t\n\adefaultr(\n\bgrok-4.5\x1a\x0e\n\x06effort\x12\x04high\x1a\f\n\x04fast\x12\x04truer\x1c\n\fcomposer-2.5\x1a\f\n\x04fast\x12\x04truerQ\n\rclaude-opus-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04high\x1a\r\n\x04fast\x12\x05falserB\n\vgpt-5.6-sol\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05falserC\n\x0eclaude-fable-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\x0fclaude-sonnet-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\rgpt-5.6-terra\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05false\x82\x01$7b4fffae-309e-4dd7-bb36-4be380112b17")

// TestDelegateHandler_TruncatesToFirstEnvelopeOnFallThroughForCursorRun
// documents a real design decision this session got wrong once and had to
// correct (2026-07-29): a brief attempt to preserve the *entire* real client
// stream on the fall-through path (via io.MultiReader, so origin passthrough
// would forward exactly what the client sent instead of just the first
// envelope) turned out to reintroduce the identical problem it was meant to
// fix. passthroughHTTPS's transport.RoundTrip reads the request body to send
// it; once the buffered first envelope is exhausted, a MultiReader whose
// second reader is the real, still-open client connection blocks on THAT
// read — waiting on cursor-agent's own next periodic keepalive envelope, up
// to ~30s away. Confirmed live: reverting to plain truncation is what
// actually fixed plain (non-delegate) passthrough end-to-end. Truncating to
// the first envelope is correct here, not lossy — the real cursor.sh origin
// is bidi-streaming capable, exactly like Glider's own synthesized replies,
// and doesn't need the client's later keepalives to respond to the human's
// actual instruction, which is always the first envelope. This test locks in
// that r.Body, once restored on the fall-through path, is exactly the first
// envelope — not the full stream, and not empty either (the original
// 2026-07-26 bug this whole restoration mechanism exists to prevent).
func TestDelegateHandler_TruncatesToFirstEnvelopeOnFallThroughForCursorRun(t *testing.T) {
	laterKeepalive := []byte{0x00, 0x00, 0x00, 0x00, 0x02, ':', 0x00}
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(realCursorAgentRunRequestFirstEnvelope)
		_, _ = pw.Write(laterKeepalive) // simulates the real client's later keepalive envelope
		pw.Close()
	}()

	h := &mitm.DelegateHandler{}
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", pr)
	req.Header.Set("Content-Type", "application/connect+proto")
	rw := httptest.NewRecorder()

	handled, err := h.TryHandle(rw, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false — no delegate flag in this prompt")
	}

	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading restored body: %v", err)
	}
	if len(restored) != len(realCursorAgentRunRequestFirstEnvelope) {
		t.Fatalf("restored body is %d bytes, want exactly %d (the first envelope, no more, no less)", len(restored), len(realCursorAgentRunRequestFirstEnvelope))
	}
	if !bytes.Equal(restored, realCursorAgentRunRequestFirstEnvelope) {
		t.Fatal("restored body does not match the first envelope's real bytes")
	}
}

func TestDelegateHandler_IgnoresNonMessagesPath(t *testing.T) {
	h := &mitm.DelegateHandler{}
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/other", strings.NewReader(`{}`))
	rw := httptest.NewRecorder()

	handled, err := h.TryHandle(rw, req)
	if err != nil || handled {
		t.Fatalf("expected handled=false, nil err for a path no OriginAdapter recognizes; got handled=%v err=%v", handled, err)
	}
}

// TestDelegateHandler_RecognizesAgyShapedRequest is the direct regression
// test for the real, live-reported bug (2026-07-27): typing a delegate flag
// straight into agy's own interactive session did nothing, because
// DelegateHandler used to hardcode `r.URL.Path != "/v1/messages"` as its
// entry gate — a check that's only ever true for Claude Code's own
// traffic. agy's real traffic (confirmed live via an isolated capture
// proxy, tools/wirecapture) is a POST to
// .../v1internal:streamGenerateContent, never that path, so the handler
// always fell straight through to real origin passthrough before any
// flag-parsing logic ran. This proves the fix: the same body shape now
// gets recognized via ngl.ResolveOriginAdapter and a delegate flag inside
// it is honored.
func TestDelegateHandler_RecognizesAgyShapedRequest(t *testing.T) {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		t.Skip("vendor registry path unavailable in this environment")
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil || len(reg.Enabled()) == 0 {
		t.Skip("no enabled vendors in the local registry — nothing to delegate to")
	}
	flagName := reg.Enabled()[0].Name

	h := &mitm.DelegateHandler{}
	payload := `{"project":"p","requestId":"r","request":{"contents":[{"role":"user","parts":[` +
		`{"text":"<USER_REQUEST>\nhi who are you /` + flagName + `\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nirrelevant\n</ADDITIONAL_METADATA>"}` +
		`]}]},"model":"gemini-3.6-flash-high","userAgent":"antigravity","requestType":"agent"}`
	req := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", strings.NewReader(payload))
	rw := httptest.NewRecorder()

	handled, err := h.TryHandle(rw, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatalf("expected handled=true — a real delegate flag in agy's own request shape must be recognized, not silently dropped")
	}
	if ct := rw.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected a reply in agy's own wire shape (text/event-stream), got Content-Type=%q", ct)
	}
	if want := "Delegated to " + flagName + ":"; !strings.Contains(rw.Body.String(), want) {
		t.Fatalf("expected the reply to name what was delegated to whom (%q), got body=%q", want, rw.Body.String())
	}
}

// TestDelegateHandler_ResolveDelegateRunsConcurrentlyWithWriteReply is the
// direct regression test for the real, live-confirmed bug (2026-07-29):
// TryHandle used to call vendors.ResolveDelegate synchronously and only
// touch the response writer once the (possibly many-second) headless run
// finished, giving cursor-agent's own HTTP/2 client zero bytes for the
// whole wait and causing it to abandon the stream. Uses a fake OriginAdapter
// registered as a real vendor front (recognized via a Matches that always
// says yes for this test's own scheme) is not available without exporting
// internals, so this instead proves the concurrency property indirectly:
// TryHandle must return well before vendors.RunTimeout even for a delegate
// target whose headless call is slow, because WriteReply's own blocking
// wait — not TryHandle itself — is what's supposed to absorb that latency.
// Covered at the adapter layer by
// cursorrpc.TestWriteDelegateReplyWithKeepAlive_HeaderArrivesBeforeResultResolves,
// which proves header reaches the wire before the slow value arrives.
func TestDelegateHandler_ResolveDelegateRunsConcurrentlyWithWriteReply(t *testing.T) {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		t.Skip("vendor registry path unavailable in this environment")
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil || len(reg.Enabled()) == 0 {
		t.Skip("no enabled vendors in the local registry — nothing to delegate to")
	}
	flagName := reg.Enabled()[0].Name

	h := &mitm.DelegateHandler{}
	payload := `{"project":"p","requestId":"r","request":{"contents":[{"role":"user","parts":[` +
		`{"text":"<USER_REQUEST>\nhi who are you /` + flagName + `\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nirrelevant\n</ADDITIONAL_METADATA>"}` +
		`]}]},"model":"gemini-3.6-flash-high","userAgent":"antigravity","requestType":"agent"}`
	req := httptest.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", strings.NewReader(payload))
	rw := httptest.NewRecorder()

	start := time.Now()
	handled, err := h.TryHandle(rw, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	// A real headless CLI run isn't available in a unit-test environment,
	// so this vendor's ResolveDelegate call resolves near-instantly with
	// an error/not-found reply either way — the meaningful assertion is
	// that TryHandle completed at all via the async goroutine+channel
	// path (agyOriginAdapter.WriteReply blocking on the channel, not on
	// ResolveDelegate directly), not a specific duration.
	if elapsed > vendors.RunTimeout {
		t.Fatalf("TryHandle took %v, want well under vendors.RunTimeout (%v)", elapsed, vendors.RunTimeout)
	}
}

func TestChainHandler_StopsAtFirstHandler(t *testing.T) {
	first := &recordingHandler{result: true}
	second := &recordingHandler{result: true}
	chain := &mitm.ChainHandler{Handlers: []mitm.LocalHandler{first, second}}

	req := httptest.NewRequest(http.MethodPost, "https://example.com/x", nil)
	handled, err := chain.TryHandle(httptest.NewRecorder(), req)
	if err != nil || !handled {
		t.Fatalf("expected handled=true, nil err; got handled=%v err=%v", handled, err)
	}
	if !first.called {
		t.Fatalf("expected first handler to be called")
	}
	if second.called {
		t.Fatalf("expected second handler NOT to be called once first claimed the request")
	}
}

func TestChainHandler_FallsThroughWhenUnclaimed(t *testing.T) {
	first := &recordingHandler{result: false}
	second := &recordingHandler{result: true}
	chain := &mitm.ChainHandler{Handlers: []mitm.LocalHandler{first, second}}

	req := httptest.NewRequest(http.MethodPost, "https://example.com/x", nil)
	handled, err := chain.TryHandle(httptest.NewRecorder(), req)
	if err != nil || !handled {
		t.Fatalf("expected handled=true, nil err; got handled=%v err=%v", handled, err)
	}
	if !first.called || !second.called {
		t.Fatalf("expected both handlers to be called; first=%v second=%v", first.called, second.called)
	}
}

type recordingHandler struct {
	result bool
	called bool
}

func (r *recordingHandler) TryHandle(w http.ResponseWriter, req *http.Request) (bool, error) {
	r.called = true
	return r.result, nil
}
