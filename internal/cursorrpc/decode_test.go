package cursorrpc_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	aiserverv1 "github.com/everestmz/cursor-rpc/cursor/gen/aiserver/v1"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/cursorrpc"
	"google.golang.org/protobuf/proto"
)

func TestDecodeStreamChat(t *testing.T) {
	model := "gpt-4o"
	msg := &aiserverv1.GetChatRequest{
		ModelDetails: &aiserverv1.ModelDetails{ModelName: &model},
		RequestId:    "req-1",
		Conversation: []*aiserverv1.ConversationMessage{
			{Text: "hello agent", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN},
			{Text: "hi human", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_AI},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

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
	if dec.Request.Metadata.RequestID != "req-1" {
		t.Fatalf("id=%q", dec.Request.Metadata.RequestID)
	}
}

func TestDecodeStreamChatConnectEnvelope(t *testing.T) {
	model := "codellama"
	msg := &aiserverv1.GetChatRequest{
		ModelDetails: &aiserverv1.ModelDetails{ModelName: &model},
		Conversation: []*aiserverv1.ConversationMessage{
			{Text: "/local ping", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN},
		},
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var framed bytes.Buffer
	var prefix [5]byte
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(payload)))
	framed.Write(prefix[:])
	framed.Write(payload)

	dec, err := cursorrpc.DecodeChatRequest("/aiserver.v1.AiService/StreamChatWeb", framed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if dec == nil || dec.Request.Model != "codellama" {
		t.Fatalf("dec=%+v", dec)
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
	// Parse first frame
	n := binary.BigEndian.Uint32(body[1:5])
	frame := body[5 : 5+n]
	var resp aiserverv1.StreamChatResponse
	if err := proto.Unmarshal(frame, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello" {
		t.Fatalf("text=%q", resp.Text)
	}
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
