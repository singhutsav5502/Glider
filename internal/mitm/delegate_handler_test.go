package mitm_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
