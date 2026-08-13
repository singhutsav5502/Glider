package cursorrpc_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/cursorrpc"
)

// Fixtures are built as protobuf bytes here, by hand, rather than by
// marshalling a generated type.
//
// That is deliberate and it is stronger than the version before it. The
// decoder under test reads the wire by field number; a fixture produced from
// the same generated type would agree with it even if both were wrong about
// Cursor's protocol. Writing the bytes out states the field numbers a second
// time, independently, so a mistake in either place shows up as a failure.

func pbVarint(v uint64) []byte {
	var b []byte
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func pbBytesField(field int, payload []byte) []byte {
	out := pbVarint(uint64(field)<<3 | 2)
	out = append(out, pbVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func pbStringField(field int, s string) []byte { return pbBytesField(field, []byte(s)) }

func pbVarintField(field int, v uint64) []byte {
	out := pbVarint(uint64(field)<<3 | 0)
	return append(out, pbVarint(v)...)
}

// ConversationMessage: text = 1, type = 2. Type 1 is HUMAN, 2 is AI.
func convMessage(text string, msgType uint64) []byte {
	out := pbStringField(1, text)
	return append(out, pbVarintField(2, msgType)...)
}

// GetChatRequest: current_file = 1, conversation = 2, explicit_context = 4,
// model_details = 7, request_id = 9. ModelDetails.model_name = 1.
func getChatRequest(model, requestID string, msgs ...[]byte) []byte {
	var out []byte
	for _, m := range msgs {
		out = append(out, pbBytesField(2, m)...)
	}
	if model != "" {
		out = append(out, pbBytesField(7, pbStringField(1, model))...)
	}
	if requestID != "" {
		out = append(out, pbStringField(9, requestID)...)
	}
	return out
}

// GetComposerChatRequest: conversation = 1, explicit_context = 3,
// model_details = 5.
func getComposerChatRequest(model string, msgs ...[]byte) []byte {
	var out []byte
	for _, m := range msgs {
		out = append(out, pbBytesField(1, m)...)
	}
	if model != "" {
		out = append(out, pbBytesField(5, pbStringField(1, model))...)
	}
	return out
}

func connectFrame(payload []byte) []byte {
	var framed bytes.Buffer
	var prefix [5]byte
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	framed.Write(prefix[:])
	framed.Write(payload)
	return framed.Bytes()
}

func TestDecodeStreamChat(t *testing.T) {
	raw := getChatRequest("gpt-4o", "req-1",
		convMessage("hello agent", 1), // HUMAN
		convMessage("hi human", 2),    // AI
	)

	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamChat", raw)
	if err != nil {
		t.Fatal(err)
	}
	if dec == nil || !dec.Fulfillable() {
		t.Fatal("expected fulfillable decode")
	}
	if dec.Request.Model != "gpt-4o" {
		t.Fatalf("model=%q", dec.Request.Model)
	}
	if len(dec.Request.Messages) != 2 {
		t.Fatalf("messages=%d", len(dec.Request.Messages))
	}
	if dec.Request.Messages[0].Role != "user" || dec.Request.Messages[0].Content != "hello agent" {
		t.Fatalf("msg0=%+v", dec.Request.Messages[0])
	}
	if dec.Request.Messages[1].Role != "assistant" || dec.Request.Messages[1].Content != "hi human" {
		t.Fatalf("msg1=%+v", dec.Request.Messages[1])
	}
	if dec.Request.Metadata.RequestID != "req-1" {
		t.Fatalf("id=%q", dec.Request.Metadata.RequestID)
	}
}

func TestDecodeStreamChatConnectEnvelope(t *testing.T) {
	payload := getChatRequest("codellama", "", convMessage("/local ping", 1))

	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamChatWeb", connectFrame(payload))
	if err != nil {
		t.Fatal(err)
	}
	if dec == nil || dec.Request.Model != "codellama" {
		t.Fatalf("dec=%+v", dec)
	}
}

func TestDecodeStreamComposer(t *testing.T) {
	// The composer request numbers its fields differently from the chat
	// request. Decoding it with the chat numbers would silently produce an
	// empty model and no messages, so this asserts both.
	payload := getComposerChatRequest("claude-3-5-sonnet", convMessage("refactor this", 1))

	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamComposer", payload)
	if err != nil {
		t.Fatal(err)
	}
	if dec == nil || !dec.Fulfillable() {
		t.Fatal("expected fulfillable composer decode")
	}
	if dec.Request.Model != "claude-3-5-sonnet" {
		t.Fatalf("model=%q", dec.Request.Model)
	}
	if len(dec.Request.Messages) != 1 || dec.Request.Messages[0].Content != "refactor this" {
		t.Fatalf("messages=%+v", dec.Request.Messages)
	}
}

func TestDecodeStreamComposerContextIsNotFulfillable(t *testing.T) {
	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamComposerContext", nil)
	if err != nil {
		t.Fatal(err)
	}
	if dec != nil {
		t.Fatal("StreamComposerContext has a different request type and must not decode")
	}
	if cursorrpc.IsFulfillableAgentPath("/aiserver.v1.AiService/StreamComposerContext") {
		t.Fatal("StreamComposerContext must not report fulfillable")
	}
}

func TestDecodeBidiOpaque(t *testing.T) {
	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.BidiService/BidiAppend", []byte{0x00, 0x01, 0x02})
	if err != nil {
		t.Fatal(err)
	}
	if dec != nil {
		t.Fatal("BidiAppend must remain opaque without schema")
	}
	if cursorrpc.IsFulfillableAgentPath("/aiserver.v1.BidiService/BidiAppend") {
		t.Fatal("BidiAppend not fulfillable")
	}
}

func TestDecodeRejectsMalformedBody(t *testing.T) {
	// A body that is not protobuf at all must be an error, not a silently
	// empty request. The framing check is what separates "not a message" from
	// "a message whose fields I skip".
	_, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamChat",
		[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if err == nil {
		t.Fatal("expected an error for a body that is not a protobuf message")
	}
}

func TestDecodeSkipsUnknownFields(t *testing.T) {
	// Cursor adding a field must not break the parse.
	raw := getChatRequest("gpt-4o", "", convMessage("hi", 1))
	raw = append(raw, pbStringField(23, "something new")...)
	raw = append(raw, pbVarintField(24, 99)...)

	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamChat", raw)
	if err != nil {
		t.Fatal(err)
	}
	if dec == nil || dec.Request.Model != "gpt-4o" || len(dec.Request.Messages) != 1 {
		t.Fatalf("unknown fields changed the decode: %+v", dec)
	}
}

func TestWriteStreamChatResponse(t *testing.T) {
	ch := make(chan backend.CompletionChunk, 2)
	ch <- backend.CompletionChunk{Content: "Hello"}
	ch <- backend.CompletionChunk{Content: " world", FinishReason: "stop"}
	close(ch)

	rr := httptest.NewRecorder()
	if err := cursorrpc.WriteStreamChatResponse(rr, ch); err != nil {
		t.Fatal(err)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/connect+proto" {
		t.Fatalf("content-type=%q", ct)
	}
	body := rr.Body.Bytes()
	if len(body) < 10 {
		t.Fatalf("short body %d", len(body))
	}

	// First frame: StreamChatResponse{ text = 1 }.
	n := binary.BigEndian.Uint32(body[1:5])
	frame := body[5 : 5+n]
	text, err := readStringField(frame, 1)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello" {
		t.Fatalf("text=%q", text)
	}
}

// readStringField pulls one length-delimited string out of a protobuf message,
// so the encoder is checked against bytes rather than against itself.
func readStringField(b []byte, want int) (string, error) {
	off := 0
	for off < len(b) {
		key, n := readTestVarint(b[off:])
		if n <= 0 {
			return "", io.ErrUnexpectedEOF
		}
		off += n
		field := int(key >> 3)
		switch key & 0x7 {
		case 2:
			ln, n := readTestVarint(b[off:])
			if n <= 0 || off+n+int(ln) > len(b) {
				return "", io.ErrUnexpectedEOF
			}
			off += n
			val := b[off : off+int(ln)]
			off += int(ln)
			if field == want {
				return string(val), nil
			}
		case 0:
			_, n := readTestVarint(b[off:])
			if n <= 0 {
				return "", io.ErrUnexpectedEOF
			}
			off += n
		default:
			return "", io.ErrUnexpectedEOF
		}
	}
	return "", nil
}

func readTestVarint(b []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i := 0; i < len(b); i++ {
		v |= uint64(b[i]&0x7f) << shift
		if b[i] < 0x80 {
			return v, i + 1
		}
		shift += 7
		if shift > 63 {
			return 0, -1
		}
	}
	return 0, -1
}

func TestFailClosedConnect(t *testing.T) {
	rr := httptest.NewRecorder()
	if err := cursorrpc.FailClosedConnect(rr, "no codec"); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if len(body) < 5 || body[0]&0b10 == 0 {
		t.Fatalf("expected end-stream flag, body=%v", body[:min(8, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
