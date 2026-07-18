package cursorrpc

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
)

// Minimal agent.v1 wire codec for RunSSE (AgentServerMessage stream).
// Schema source: cursor-tap cursor_proto/agent_v1.proto (observe-only prior art).
//
// AgentServerMessage {
//   InteractionUpdate interaction_update = 1;
//   ConversationStateStructure conversation_checkpoint_update = 3;
// }
// InteractionUpdate oneof:
//   TextDeltaUpdate text_delta = 1;   // { string text = 1 }
//   TokenDeltaUpdate token_delta = 8; // { int32 tokens = 1 }
//   HeartbeatUpdate heartbeat = 13;  // {}
//   TurnEndedUpdate turn_ended = 14; // {}
//
// Observed live heartbeat payload: 0a026a00

const (
	runSSEKindHeartbeat          = "heartbeat"
	runSSEKindTextDelta          = "text_delta"
	runSSEKindTokenDelta         = "token_delta"
	runSSEKindTurnEnded          = "turn_ended"
	runSSEKindCheckpoint         = "conversation_checkpoint"
	runSSEKindThinkingDelta      = "thinking_delta"
	runSSEKindThinkingCompleted  = "thinking_completed"
	runSSEKindUserMessageAppend  = "user_message_appended"
	runSSEKindKvServer           = "kv_server_message"
	runSSEKindOtherInteraction   = "interaction_other"
	runSSEKindOtherServerMessage = "server_other"
	runSSEKindCompressedEncrypted = "compressed_or_opaque"
)

// EncodeAgentHeartbeat returns AgentServerMessage{interaction_update: heartbeat{}}.
func EncodeAgentHeartbeat() []byte {
	// 0a 02 6a 00
	return []byte{0x0a, 0x02, 0x6a, 0x00}
}

// EncodeAgentTurnEnded returns AgentServerMessage{interaction_update: turn_ended{}}.
func EncodeAgentTurnEnded() []byte {
	// 0a 02 72 00
	return []byte{0x0a, 0x02, 0x72, 0x00}
}

// EncodeAgentTextDelta returns AgentServerMessage{interaction_update: text_delta{text}}.
func EncodeAgentTextDelta(text string) []byte {
	inner := protoBytesField(1, []byte(text)) // TextDeltaUpdate.text
	delta := protoBytesField(1, inner)        // InteractionUpdate.text_delta
	return protoBytesField(1, delta)          // AgentServerMessage.interaction_update
}

// EncodeAgentTokenDelta returns AgentServerMessage{interaction_update: token_delta{tokens}}.
func EncodeAgentTokenDelta(tokens int32) []byte {
	td := protoVarintField(1, uint64(tokens)) // TokenDeltaUpdate.tokens
	iu := protoBytesField(8, td)              // InteractionUpdate.token_delta = 8
	return protoBytesField(1, iu)
}

// EncodeAgentThinkingDelta returns AgentServerMessage{interaction_update: thinking_delta}.
// Live origin emits these before text_delta (InteractionUpdate field 4).
func EncodeAgentThinkingDelta(text string, style int32) []byte {
	inner := protoBytesField(1, []byte(text))
	if style > 0 {
		inner = append(inner, protoVarintField(2, uint64(style))...)
	}
	iu := protoBytesField(4, inner) // InteractionUpdate.thinking_delta = 4
	return protoBytesField(1, iu)
}

// EncodeAgentEmptyCheckpoint returns AgentServerMessage{conversation_checkpoint_update: {}}.
// Live peeks rarely show field 3 early; origin prefers KvServerMessage (field 4). Kept for
// fixtures / experiments — WriteRunSSETextResponse no longer emits it by default.
func EncodeAgentEmptyCheckpoint() []byte {
	return protoBytesField(3, nil) // field 3, empty ConversationStateStructure
}

func protoBytesField(field int, payload []byte) []byte {
	tag := uint64(field<<3 | 2)
	out := appendVarint(nil, tag)
	out = appendVarint(out, uint64(len(payload)))
	return append(out, payload...)
}

func protoVarintField(field int, v uint64) []byte {
	tag := uint64(field<<3 | 0)
	out := appendVarint(nil, tag)
	return appendVarint(out, v)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// WriteRunSSETextResponse encodes a text-only Agent RunSSE stream matching the
// observed origin shape (minus KV blobs):
//
//	heartbeat → thinking_delta → text_delta(s) + token_delta → turn_ended → Connect end
//
// Content-Type matches Connect streaming (application/connect+proto). Frames are
// uncompressed (flags=0); clients advertise Connect-Accept-Encoding: gzip but
// uncompressed is always valid.
//
// Ground truth (ping-glider-5 RESP peeks): early large frames are KvServerMessage
// (field 4), not conversation_checkpoint (field 3). InteractionUpdate field 4 =
// thinking_delta precedes text_delta. Empty field-3 checkpoint is omitted.
func WriteRunSSETextResponse(w http.ResponseWriter, chunks <-chan backend.CompletionChunk) error {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	if err := writeEnvelope(w, 0, EncodeAgentHeartbeat()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	// Brief thinking delta mirrors origin (style DEFAULT=1) before assistant text.
	if err := writeEnvelope(w, 0, EncodeAgentThinkingDelta(" ", 1)); err != nil {
		return err
	}
	if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(1)); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	var sentTokens int32
	for chunk := range chunks {
		if chunk.Content != "" {
			if err := writeEnvelope(w, 0, EncodeAgentTextDelta(chunk.Content)); err != nil {
				return err
			}
			// Rough token bump so UI progress looks alive (not billed; cosmetic).
			approx := int32(len([]rune(chunk.Content))/4 + 1)
			sentTokens += approx
			if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(approx)); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if chunk.FinishReason != "" && chunk.Content == "" {
			break
		}
	}

	if err := writeEnvelope(w, 0, EncodeAgentTurnEnded()); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	_ = sentTokens
	return WriteConnectEndStream(w)
}

// CannedCompletionChunks returns a closed channel with one text chunk (for dry-run fulfill).
func CannedCompletionChunks(text string) <-chan backend.CompletionChunk {
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: text, FinishReason: "stop", Model: "glider-canned"}
	close(ch)
	return ch
}

// RunSSEFrameInfo is one decoded Connect frame from a RunSSE response peek.
type RunSSEFrameInfo struct {
	Index      int    `json:"index"`
	Flags      uint8  `json:"flags"`
	WireBytes  int    `json:"wire_bytes"`
	Inflated   int    `json:"inflated_bytes,omitempty"`
	Kind       string `json:"kind"`
	ProtoWire  string `json:"proto_wire,omitempty"`
	TextHint   string `json:"text_hint,omitempty"`
	Compressed bool   `json:"compressed,omitempty"`
}

// ClassifyRunSSEPayload classifies an uncompressed AgentServerMessage payload.
func ClassifyRunSSEPayload(payload []byte) (kind, wire, textHint string) {
	wire = summarizeWire(payload, 16)
	if len(payload) == 0 {
		return runSSEKindOtherServerMessage, wire, ""
	}
	// Top-level field walk.
	off := 0
	key, n := readVarint(payload[off:])
	if n <= 0 {
		return runSSEKindOtherServerMessage, wire, ""
	}
	off += n
	fieldNum := int(key >> 3)
	wireType := int(key & 7)
	if wireType != 2 {
		return runSSEKindOtherServerMessage, wire, ""
	}
	l, n := readVarint(payload[off:])
	if n <= 0 {
		return runSSEKindOtherServerMessage, wire, ""
	}
	off += n
	if off+int(l) > len(payload) {
		return runSSEKindOtherServerMessage, wire, ""
	}
	inner := payload[off : off+int(l)]

	switch fieldNum {
	case 3:
		return runSSEKindCheckpoint, wire, ""
	case 4:
		return runSSEKindKvServer, wire, ""
	case 1:
		return classifyInteractionUpdate(inner, wire)
	default:
		return runSSEKindOtherServerMessage, wire, ""
	}
}

func classifyInteractionUpdate(inner []byte, outerWire string) (kind, wire, textHint string) {
	wire = outerWire
	if len(inner) == 0 {
		return runSSEKindOtherInteraction, wire, ""
	}
	key, n := readVarint(inner)
	if n <= 0 {
		return runSSEKindOtherInteraction, wire, ""
	}
	fieldNum := int(key >> 3)
	wireType := int(key & 7)
	rest := inner[n:]
	switch fieldNum {
	case 1: // text_delta
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
				if textHint == "" {
					textHint = printableHint(s, 96)
				}
			}
		}
		return runSSEKindTextDelta, wire, textHint
	case 4: // thinking_delta
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
				if textHint == "" {
					textHint = printableHint(s, 96)
				}
			}
		}
		return runSSEKindThinkingDelta, wire, textHint
	case 5: // thinking_completed
		return runSSEKindThinkingCompleted, wire, ""
	case 6: // user_message_appended
		return runSSEKindUserMessageAppend, wire, ""
	case 8: // token_delta
		return runSSEKindTokenDelta, wire, ""
	case 13: // heartbeat
		return runSSEKindHeartbeat, wire, ""
	case 14: // turn_ended
		return runSSEKindTurnEnded, wire, ""
	default:
		return runSSEKindOtherInteraction, wire, ""
	}
}

func readLDPayload(b []byte) ([]byte, bool) {
	l, n := readVarint(b)
	if n <= 0 || n+int(l) > len(b) {
		return nil, false
	}
	return b[n : n+int(l)], true
}

// InspectRunSSEResponseDeep walks Connect frames, gunzips compressed ones, and
// classifies AgentServerMessage oneofs.
func InspectRunSSEResponseDeep(peek []byte, statusCode int, contentType string) *RunSSEResponseInspect {
	out := inspectRunSSEResponseShallow(peek, statusCode, contentType)
	if out == nil || len(peek) == 0 {
		return out
	}
	frames := scanConnectFramesLocal(peek)
	infos := make([]RunSSEFrameInfo, 0, len(frames))
	var texts []string
	off := 0
	for i, f := range frames {
		if off+5 > len(peek) {
			break
		}
		size := f.size
		payloadOff := off + 5
		off = payloadOff + size
		if payloadOff > len(peek) {
			break
		}
		payload := peek[payloadOff:]
		if len(payload) > size {
			payload = payload[:size]
		}
		info := RunSSEFrameInfo{
			Index:      i,
			Flags:      f.flags,
			WireBytes:  size,
			Compressed: f.flags&flagCompressed != 0,
		}
		raw := payload
		if info.Compressed {
			gr, err := gzip.NewReader(bytes.NewReader(payload))
			if err != nil {
				info.Kind = runSSEKindCompressedEncrypted
				infos = append(infos, info)
				continue
			}
			inflated, err := io.ReadAll(io.LimitReader(gr, 512<<10))
			_ = gr.Close()
			if err != nil && err != io.EOF && len(inflated) == 0 {
				info.Kind = runSSEKindCompressedEncrypted
				infos = append(infos, info)
				continue
			}
			raw = inflated
			info.Inflated = len(inflated)
		}
		kind, wire, hint := ClassifyRunSSEPayload(raw)
		info.Kind = kind
		info.ProtoWire = wire
		info.TextHint = hint
		if hint != "" && len(texts) < 12 {
			texts = append(texts, truncateRunes(hint, 80))
		}
		infos = append(infos, info)
	}
	out.Frames = infos
	if len(texts) > 0 {
		out.Strings = texts
		out.PrintableHint = texts[0]
	}
	// Prefer first interesting proto wire for summary line.
	for _, fr := range infos {
		if fr.Kind == runSSEKindTextDelta || fr.Kind == runSSEKindHeartbeat || fr.Kind == runSSEKindTurnEnded {
			out.ProtoWire = fr.ProtoWire
			break
		}
	}
	return out
}

// HasRunSSETextCodec reports that Glider can author a minimal text-only RunSSE stream.
func HasRunSSETextCodec() bool { return true }

// HasRunSSEToolCodec reports whether Path B can fulfill child/tool-loop RunSSE
// (tool_call frames). False until the tool response codec lands — callers still
// re-decide via routing.tool_followup and emit tool_followup_would_local.
func HasRunSSEToolCodec() bool { return false }

// IsRootAgentRunSSE reports a root Agent turn (no parent/child correlation headers).
// Cursor Task subagents and tool-loop children often set one of these; treat them
// as non-root so Path B does not local-fulfill mid-turn.
func IsRootAgentRunSSE(r *http.Request) bool {
	return !IsChildAgentRequest(r)
}

// IsChildAgentRequest reports parent/tool/subagent correlation headers.
func IsChildAgentRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, k := range []string{
		"X-Parent-Agent-Tool-Call-Id",
		"X-Parent-Request-Id",
		"X-Root-Parent-Request-Id",
		"X-Parent-Agent-Request-Id",
	} {
		if strings.TrimSpace(r.Header.Get(k)) != "" {
			return true
		}
	}
	return false
}

// RequestIDFromHeaders prefers X-Request-Id / X-Original-Request-Id for correlation.
func RequestIDFromHeaders(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, k := range []string{"X-Request-Id", "X-Original-Request-Id"} {
		if v := strings.TrimSpace(r.Header.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

// ExtractRunSSERequestID returns the BidiRequestId.request_id from a RunSSE body.
func ExtractRunSSERequestID(body []byte) string {
	insp, err := InspectRunSSE(body)
	if err != nil || insp == nil {
		return ""
	}
	return insp.RequestID
}

// SynthesizeRunSSEResponseFixture builds an anonymized multi-frame body for tests.
func SynthesizeRunSSEResponseFixture(text string) []byte {
	var buf bytes.Buffer
	_ = writeEnvelope(&buf, 0, EncodeAgentHeartbeat())
	_ = writeEnvelope(&buf, 0, EncodeAgentThinkingDelta(" ", 1))
	_ = writeEnvelope(&buf, 0, EncodeAgentTokenDelta(1))
	_ = writeEnvelope(&buf, 0, EncodeAgentTextDelta(text))
	_ = writeEnvelope(&buf, 0, EncodeAgentTokenDelta(3))
	_ = writeEnvelope(&buf, 0, EncodeAgentTurnEnded())
	_ = writeEnvelope(&buf, flagEndStream, []byte("{}"))
	return buf.Bytes()
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
