package ngl_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// realCursorAgentRunRequestBody is byte-for-byte the request body captured
// live 2026-07-27 (via tools/wirecapture's genuine HTTP/2 support) from a
// real `cursor-agent -p "reply with exactly: CURSORCAP_OK" --trust`
// headless run — POST /agent.v1.AgentService/Run to
// agentn.global.api5.cursor.sh. Not synthesized; this is the actual wire
// capture, byte-for-byte (extracted via Go's strconv.Unquote from the raw
// dump file, not hand-retyped, to avoid transcription error).
var realCursorAgentRunRequestBody = []byte("\x00\x00\x00\x02n\n\xeb\x04\n\x00\x12P\nN\nL\n reply with exactly: CURSORCAP_OK\x12$ba5ebf39-21c4-47c8-bc09-e1bee5311657\x1a\x00 \x01\"\x00*$7b4fffae-309e-4dd7-bb36-4be380112b17J\t\n\adefault`\x00r\t\n\adefaultr(\n\bgrok-4.5\x1a\x0e\n\x06effort\x12\x04high\x1a\f\n\x04fast\x12\x04truer\x1c\n\fcomposer-2.5\x1a\f\n\x04fast\x12\x04truerQ\n\rclaude-opus-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04high\x1a\r\n\x04fast\x12\x05falserB\n\vgpt-5.6-sol\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05falserC\n\x0eclaude-fable-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\x0fclaude-sonnet-5\x1a\x10\n\bthinking\x12\x04true\x1a\x0f\n\acontext\x12\x04300k\x1a\x0e\n\x06effort\x12\x04highrD\n\rgpt-5.6-terra\x1a\x0f\n\acontext\x12\x04272k\x1a\x13\n\treasoning\x12\x06medium\x1a\r\n\x04fast\x12\x05false\x82\x01$7b4fffae-309e-4dd7-bb36-4be380112b17\x00\x00\x00\x00\x02:\x00\x00\x00\x00\x00\x02:\x00\x00\x00\x00\x00\x02:\x00\x00\x00\x00\x00\x02:\x00\x00\x00\x00\x00\x02:\x00")

func TestResolveOriginAdapter_CursorAgentRun(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", nil)
	a := ngl.ResolveOriginAdapter(req)
	if a == nil || a.Vendor() != "cursor-agent" {
		t.Fatalf("expected cursor-agent adapter for AgentService/Run, got %v", a)
	}
}

// TestResolveOriginAdapter_CursorAgentRun_HostIncludesPort is the direct
// regression test for a real, live-confirmed bug (2026-07-28): under
// transparent interception, cursor-agent's own HTTP/2 client sends an
// :authority pseudo-header that includes the port explicitly
// ("agentn.global.api5.cursor.sh:443", not the bare hostname) — unlike
// httptest.NewRequest's own default, and unlike what the CONNECT-based
// gateway path happens to see. Matches() used to compare r.Host directly
// against a bare-hostname suffix, so it silently never fired for real
// transparent traffic even though the request genuinely reached Glider —
// no error, just an unclaimed request falling through to origin
// passthrough. This test sets Host explicitly to the with-port form the
// existing test above never exercises.
func TestResolveOriginAdapter_CursorAgentRun_HostIncludesPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", nil)
	req.Host = "agentn.global.api5.cursor.sh:443"
	a := ngl.ResolveOriginAdapter(req)
	if a == nil || a.Vendor() != "cursor-agent" {
		t.Fatalf("expected cursor-agent adapter to match even with an explicit :443 in Host, got %v", a)
	}
}

func TestCursorOriginAdapter_ExtractUserInstruction_RealCapturedBytes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", nil)
	adapter := ngl.ResolveOriginAdapter(req)
	if adapter == nil {
		t.Fatal("expected cursor-agent adapter to be resolved")
	}

	text, _, stream, ok, err := adapter.ExtractUserInstruction(realCursorAgentRunRequestBody)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true decoding a real captured AgentService/Run request")
	}
	if text != "reply with exactly: CURSORCAP_OK" {
		t.Fatalf("got text %q, want the real human prompt from the live capture", text)
	}
	if !stream {
		t.Fatal("expected stream=true — AgentService.Run always returns a stream")
	}
}

func TestCursorOriginAdapter_ExtractUserInstruction_RefusesUnrelatedShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", nil)
	adapter := ngl.ResolveOriginAdapter(req)

	_, _, _, ok, err := adapter.ExtractUserInstruction([]byte("not protobuf at all, just text"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a body that doesn't decode to the confirmed field path")
	}
}

func TestCursorOriginAdapter_WriteReply_ProducesConnectFramedStream(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", nil)
	adapter := ngl.ResolveOriginAdapter(req)

	rw := httptest.NewRecorder()
	if err := adapter.WriteReply(rw, "", "hello from glider", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct := rw.Header().Get("Content-Type"); ct != "application/connect+proto" {
		t.Fatalf("got Content-Type %q, want application/connect+proto", ct)
	}
	body := rw.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("expected a non-empty Connect-framed response body")
	}
	if !strings.Contains(string(body), "hello from glider") {
		t.Fatalf("expected the reply text embedded in the encoded text_delta frame")
	}
}
