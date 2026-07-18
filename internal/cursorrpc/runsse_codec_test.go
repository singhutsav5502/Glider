package cursorrpc_test

import (
	"bytes"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/cursorrpc"
)

func TestEncodeAgentHeartbeatWire(t *testing.T) {
	got := cursorrpc.EncodeAgentHeartbeat()
	want := []byte{0x0a, 0x02, 0x6a, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
	kind, _, _ := cursorrpc.ClassifyRunSSEPayload(got)
	if kind != "heartbeat" {
		t.Fatalf("kind=%s", kind)
	}
}

func TestEncodeAgentTextDeltaRoundTrip(t *testing.T) {
	payload := cursorrpc.EncodeAgentTextDelta("ping-glider")
	kind, _, hint := cursorrpc.ClassifyRunSSEPayload(payload)
	if kind != "text_delta" {
		t.Fatalf("kind=%s wire payload=%x", kind, payload)
	}
	if hint != "ping-glider" {
		t.Fatalf("hint=%q", hint)
	}
}

func TestEncodeAgentTurnEnded(t *testing.T) {
	got := cursorrpc.EncodeAgentTurnEnded()
	kind, _, _ := cursorrpc.ClassifyRunSSEPayload(got)
	if kind != "turn_ended" {
		t.Fatalf("kind=%s hex=%x", kind, got)
	}
}

func TestSynthesizeAndInspectRunSSEFixture(t *testing.T) {
	body := cursorrpc.SynthesizeRunSSEResponseFixture("hello-local")
	insp := cursorrpc.InspectRunSSEResponseBody(body, 200, "application/connect+proto")
	if insp == nil || insp.FrameCount < 5 {
		t.Fatalf("%+v", insp)
	}
	joined := ""
	for _, f := range insp.Frames {
		joined += f.Kind + ","
	}
	for _, want := range []string{"heartbeat", "thinking_delta", "text_delta", "token_delta", "turn_ended"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if insp.PrintableHint != "hello-local" && !strings.Contains(strings.Join(insp.Strings, "|"), "hello-local") {
		t.Fatalf("hint=%q strings=%v", insp.PrintableHint, insp.Strings)
	}
}

func TestEncodeAgentThinkingDelta(t *testing.T) {
	payload := cursorrpc.EncodeAgentThinkingDelta("The user likely tested", 1)
	kind, _, hint := cursorrpc.ClassifyRunSSEPayload(payload)
	if kind != "thinking_delta" {
		t.Fatalf("kind=%s hex=%x", kind, payload)
	}
	if hint != "The user likely tested" {
		t.Fatalf("hint=%q", hint)
	}
}

func TestClassifyKvServerMessage(t *testing.T) {
	// AgentServerMessage field 4 (KvServerMessage) — live peeks start with 0x22…
	payload := []byte{0x22, 0x02, 0x08, 0x01} // field4 ld(2) = varint field1=1
	kind, _, _ := cursorrpc.ClassifyRunSSEPayload(payload)
	if kind != "kv_server_message" {
		t.Fatalf("kind=%s", kind)
	}
}

func TestWriteRunSSETextResponse(t *testing.T) {
	ch := make(chan backend.CompletionChunk, 2)
	ch <- backend.CompletionChunk{Content: "Hi "}
	ch <- backend.CompletionChunk{Content: "there", FinishReason: "stop"}
	close(ch)

	rr := httptest.NewRecorder()
	if err := cursorrpc.WriteRunSSETextResponse(rr, ch); err != nil {
		t.Fatal(err)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/connect+proto" {
		t.Fatalf("ct=%s", ct)
	}
	insp := cursorrpc.InspectRunSSEResponseBody(rr.Body.Bytes(), 200, "application/connect+proto")
	foundText := false
	for _, f := range insp.Frames {
		if f.Kind == "text_delta" && (f.TextHint == "Hi " || f.TextHint == "there") {
			foundText = true
		}
	}
	if !foundText {
		t.Fatalf("frames=%+v", insp.Frames)
	}
}

func TestInspectSynthFixtureFile(t *testing.T) {
	raw, err := os.ReadFile("testdata/runsse_resp_text_synth.bin")
	if err != nil {
		t.Fatal(err)
	}
	insp := cursorrpc.InspectRunSSEResponseBody(raw, 200, "application/connect+proto")
	if insp.FrameCount < 5 {
		t.Fatalf("%+v", insp)
	}
	found := false
	for _, f := range insp.Frames {
		if f.Kind == "text_delta" && f.TextHint == "fixture-hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("frames=%+v", insp.Frames)
	}
}
