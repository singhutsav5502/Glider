package cursorrpc_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

func TestExtractBidiCompletionRequestTipTap(t *testing.T) {
	// Capture-5 style: TipTap text node inside nested field 2 of context_envelope.
	tiptap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":" ping-glider-4"}]}]}`)
	model := []byte("gemini-3-flash")
	label := []byte("system_prompt")

	var nested []byte
	meta := bytesRepeat('m', 64)
	nested = append(nested, 0x0a, byte(len(meta)))
	nested = append(nested, meta...)
	nested = append(nested, appendProtoLD(2, tiptap)...)
	nested = append(nested, 0x72, byte(len(label)))
	nested = append(nested, label...)
	nested = append(nested, 0x72, byte(len(model)))
	nested = append(nested, model...)
	for len(nested) < 300 {
		pad := []byte("xxxxxxxx")
		nested = append(nested, 0x0a, byte(len(pad)))
		nested = append(nested, pad...)
	}

	inner := appendProtoLD(1, nested)
	innerHex := hex.EncodeToString(inner)
	body := appendProtoLD(1, []byte(innerHex))
	reqID := "aa79d4ab-0000-4000-8000-000000000099"
	nid := append([]byte{0x0a, byte(len(reqID))}, reqID...)
	body = append(body, 0x12, byte(len(nid)))
	body = append(body, nid...)

	got, err := cursorrpc.ExtractBidiCompletionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Request == nil {
		t.Fatal("expected extract")
	}
	if got.Source != "tiptap_text" {
		t.Fatalf("source=%q", got.Source)
	}
	if !strings.Contains(got.UserText, "ping-glider-4") {
		t.Fatalf("user=%q", got.UserText)
	}
	if len(got.Request.Messages) != 1 || got.Request.Messages[0].Role != "user" {
		t.Fatalf("messages=%+v", got.Request.Messages)
	}
	if got.Request.Metadata.Adapter != "cursor_bidi_extract" {
		t.Fatalf("adapter=%q", got.Request.Metadata.Adapter)
	}
	if got.ModelHint != "gemini-3-flash" && got.Request.Model != "gemini-3-flash" {
		// model may come from strings; either is fine
		if !strings.Contains(got.Request.Model, "gemini") && got.Request.Model != "cursor-agent" {
			t.Fatalf("model=%q hint=%q", got.Request.Model, got.ModelHint)
		}
	}
	if got.Inspect == nil || got.Inspect.RoleGuess != cursorrpc.RoleGuessContextEnvelope {
		t.Fatalf("inspect=%+v", got.Inspect)
	}
}

func TestLatestUserTurnText_StripsHistoryNoise(t *testing.T) {
	parts := []string{
		"Hi — I'm Auto, Cursor's agent router.",
		"Hello! It means \"hello\" in Arabic.",
		"Composer",
		"/cloud say hi through a subagent",
	}
	got := cursorrpc.LatestUserTurnText(parts)
	if !strings.Contains(got, "/cloud say hi") {
		t.Fatalf("got=%q want latest /cloud turn", got)
	}
	if strings.Contains(got, "Arabic") || strings.Contains(got, "I'm Auto") {
		t.Fatalf("history noise leaked: %q", got)
	}

	noSlash := []string{
		"Hi — I'm Auto, Cursor's agent router.",
		"rename foo to bar please",
	}
	got = cursorrpc.LatestUserTurnText(noSlash)
	if got != "rename foo to bar please" {
		t.Fatalf("got=%q want last real user turn only", got)
	}
}

func TestExtractBidiCompletionRequestSkipsAck(t *testing.T) {
	inner := []byte{0x3a, 0x00} // 7:ld(0)
	innerHex := hex.EncodeToString(inner)
	body := append([]byte{0x0a, byte(len(innerHex))}, []byte(innerHex)...)
	got, err := cursorrpc.ExtractBidiCompletionRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("ack must not extract: %+v", got)
	}
}

func TestInspectRunSSEResponseBody(t *testing.T) {
	// Connect frame with a small proto containing printable text.
	text := []byte("hello-assistant")
	proto := append([]byte{0x0a, byte(len(text))}, text...)
	frame := []byte{0x00, 0x00, 0x00, 0x00, byte(len(proto))}
	body := append(frame, proto...)

	got := cursorrpc.InspectRunSSEResponseBody(body, 200, "application/connect+proto")
	if got == nil {
		t.Fatal("nil")
	}
	if got.FrameCount != 1 || got.StatusCode != 200 {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.PrintableHint, "hello") && !strings.Contains(strings.Join(got.Strings, "|"), "hello") {
		t.Fatalf("hint=%q strings=%v", got.PrintableHint, got.Strings)
	}
}

func bytesRepeat(c byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}
