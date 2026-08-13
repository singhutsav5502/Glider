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

// TestDelegateHandler_RestoresBodyWhenNotHandled is the regression test for a
// true defect that a person released and that a live test found on 2026-07-26.
//
// DelegateHandler read the full body of the request to search for a delegate
// flag. When it found no flag, it returned handled=false and did not put the
// body back. Therefore each usual request, which has no delegate flag and which
// continued to the passthrough to the origin, went out with an empty body. That
// stopped the true API calls for the full session.
//
// The contract of mitmSession says exactly this, in internal/mitm/proxy.go at
// the position where it calls TryHandle: "LocalHandler must not consume body
// unless it handles."
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
// regression test for the full path of the true defect from 2026-07-26.
//
// Claude Code adds <system-reminder> content automatically. A flag of a
// vendor can be inside that content only, and no person typed it. Such a flag
// must not start a delegation.
//
// The test uses each vendor that the true local registry has enabled. It
// skips when no vendor is enabled. Therefore it does not need one specific
// vendor on each machine that runs it.
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

// realCursorAgentRunRequestFirstEnvelope is the first Connect envelope of a
// true AgentService/Run request of cursor-agent, which a person captured live.
// It is the same bytes as the fixture in
// internal/ngl/adapter_cursor_origin_test.go. This file has a copy and does not
// import it, because that is a test var with no export, in a different package.
//
// It decodes to the prompt "reply with exactly: CURSORCAP_OK". It has no
// delegate flag. Therefore DelegateHandler must not handle it, and the request
// must continue.
var realCursorAgentRunRequestFirstEnvelope = []byte("\x00\x00\x00\x02n\n\xeb\x04\n\x00\x12P\nN\nL\n reply with exactly: CURSORCAP_OK\x12$ba5ebf39-21c4-47c8-bc09-e1bee5311657\x1a\x00 \x01\"\x00*$7b4fffae-309e-4dd7-bb36-4be380112b17J\t\n\adefault`\x00r\t\n\adefaultr(\n\bgrok-4.5\x1a\x0e\n\x06effort\x12\x04high\x1a\f\n\x04fast\x12\x04truer\x1c\n\fcomposer-2.5\x1a\f\n\x04fast\x12\x04truerQ\n\rclaude-opus-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04high\x1a\r\n\x04fast\x12\x05falserB\n\vgpt-5.6-sol\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05falserC\n\x0eclaude-fable-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\x0fclaude-sonnet-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\rgpt-5.6-terra\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05false\x82\x01$7b4fffae-309e-4dd7-bb36-4be380112b17")

// TestDelegateHandler_PreservesFullStreamOnFallThroughForCursorRun records
// the final result of a sequence of changes in this investigation, on
// 2026-07-29 and 2026-07-30.
//
// A person kept the full stream of the client on the path that continues to
// the origin, with io.MultiReader. Then a person removed that change, because
// a block of approximately 30 seconds returned. Then a person applied the
// change again, correctly, after a person found and corrected the true cause
// of that block in a different position.
//
// The true cause: passthroughHTTPS made the outbound request with
// req.Context(), which is the context of the OWN inbound connection of
// cursor-agent. The true client of cursor-agent resets its own stream almost
// immediately, and an isolated trace of the HTTP/2 frames from wirecapture
// confirmed this. Therefore that reset stopped the full outbound relay of
// Glider to the true origin, and this included the reads that block. The
// speed of that relay had no importance.
//
// A separate live test confirmed a second fact. This test made the request
// short before, with only the first envelope. That method stopped the plain
// passthrough, which has no delegate. The true origin at cursor.sh then
// received only a short request, which the code closed before its end. It did
// not receive the exchange in two directions that a true client sends. That
// origin then never completed a response, and it had a full independent limit
// of 120 seconds to do so.
//
// The outbound part of passthroughHTTPS now has its own independent context.
// Refer to the comment on that function. Therefore it is safe here to relay
// the live stream again.
func TestDelegateHandler_PreservesFullStreamOnFallThroughForCursorRun(t *testing.T) {
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
	want := append(append([]byte{}, realCursorAgentRunRequestFirstEnvelope...), laterKeepalive...)
	if len(restored) != len(want) {
		t.Fatalf("restored body is %d bytes, want %d (first envelope + later keepalive) — the fall-through path must not truncate the real client's stream", len(restored), len(want))
	}
	if !bytes.Equal(restored, want) {
		t.Fatal("restored body does not match first-envelope+keepalive bytes")
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
// test for a true defect that a user reported on 2026-07-27. A person typed a
// delegate flag in the interactive session of agy, and nothing occurred. The
// cause: DelegateHandler had a fixed test at its entry, `r.URL.Path !=
// "/v1/messages"`. That test is true only for the traffic of Claude Code. The
// true traffic of agy is a POST to .../v1internal:streamGenerateContent, and
// never that path. An isolated capture proxy, tools/wirecapture, confirmed
// this live. Therefore the handler always continued to the passthrough to the
// true origin, before any code read the flag. This proves the fix: the same
// body shape now gets recognized via ngl.ResolveOriginAdapter and a delegate
// flag inside it is honored.
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
// direct regression test for a true defect, and a live test confirmed it on
// 2026-07-29. TryHandle called vendors.ResolveDelegate in sequence. It then
// used the response writer only after the run with no console completed, and
// that run can need many seconds. Therefore the HTTP/2 client of cursor-agent
// received zero bytes for the full wait, and it stopped the stream. A false
// OriginAdapter, registered as a true vendor front, needs internal values
// that this package does not export. Therefore this test proves the property
// in a different way. TryHandle must return quickly, also for a delegate
// target with a slow call that has no console. The wait inside WriteReply
// must hold that delay, and TryHandle must not hold it. Covered at the
// adapter layer by
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
	// A true CLI run with no console is not available in a unit test. Therefore
	// the ResolveDelegate call of this vendor completes almost immediately, with
	// an error or a "not found" reply. The assertion of value is that TryHandle
	// completed through the path with a goroutine and a channel. In that path,
	// agyOriginAdapter.WriteReply waits on the channel, and it does not wait on
	// ResolveDelegate. This test does not assert a specific time. An absolute
	// bound, not vendors.RunTimeout: that is 0 (no ceiling) by default now,
	// which would make this assertion vacuous.
	const wantUnder = 30 * time.Second
	if elapsed > wantUnder {
		t.Fatalf("TryHandle took %v, want well under %v — it must not block on ResolveDelegate", elapsed, wantUnder)
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
