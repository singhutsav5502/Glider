package cursorrpc

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// Minimal agent.v1 wire codec for RunSSE (AgentServerMessage stream).
//
// AgentServerMessage {
//   InteractionUpdate interaction_update = 1;
//   ConversationStateStructure conversation_checkpoint_update = 3;
// }
// InteractionUpdate oneof:
//   TextDeltaUpdate text_delta = 1;   // { string text = 1 }
//   ToolCallStartedUpdate tool_call_started = 2;
//   ToolCallCompletedUpdate tool_call_completed = 3;
//   PartialToolCallUpdate partial_tool_call = 7;
//   TokenDeltaUpdate token_delta = 8; // { int32 tokens = 1 }
//   HeartbeatUpdate heartbeat = 13;  // {}
//   TurnEndedUpdate turn_ended = 14; // {}
//
// Observed live heartbeat payload: 0a026a00

const (
	runSSEKindHeartbeat           = "heartbeat"
	runSSEKindTextDelta           = "text_delta"
	runSSEKindTokenDelta          = "token_delta"
	runSSEKindTurnEnded           = "turn_ended"
	runSSEKindCheckpoint          = "conversation_checkpoint"
	runSSEKindThinkingDelta       = "thinking_delta"
	runSSEKindThinkingCompleted   = "thinking_completed"
	runSSEKindUserMessageAppend   = "user_message_appended"
	runSSEKindToolCallStarted     = "tool_call_started"
	runSSEKindToolCallCompleted   = "tool_call_completed"
	runSSEKindPartialToolCall     = "partial_tool_call"
	runSSEKindToolCallDelta       = "tool_call_delta"
	runSSEKindKvServer            = "kv_server_message"
	runSSEKindOtherInteraction    = "interaction_other"
	runSSEKindOtherServerMessage  = "server_other"
	runSSEKindCompressedEncrypted = "compressed_or_opaque"
)

// runSSEToolCodecEnabled gates Path B child/tool-loop fulfill. Default false so
// Mode A + text-only Path B stay unchanged until mitm.agent_rpc_tool_codec is on.
var runSSEToolCodecEnabled atomic.Bool

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

// EncodeTruncatedToolCallWire returns ToolCall{truncated_tool_call: {}} (oneof field 34).
// Fallback when EncodeMappedToolCallWire cannot map an OpenAI/Cursor tool name.
func EncodeTruncatedToolCallWire() []byte {
	// ToolCall.truncated_tool_call = 34 → empty TruncatedToolCall {}
	return protoBytesField(toolCallFieldTruncated, nil)
}

// EncodeAgentPartialToolCall returns AgentServerMessage{partial_tool_call}.
// InteractionUpdate.partial_tool_call = 7; PartialToolCallUpdate fields 1/3.
func EncodeAgentPartialToolCall(callID, argsTextDelta string) []byte {
	inner := protoBytesField(1, []byte(callID))
	if argsTextDelta != "" {
		inner = append(inner, protoBytesField(3, []byte(argsTextDelta))...)
	}
	// Attach a minimal ToolCall so clients that require field 2 still parse.
	inner = append(inner, protoBytesField(2, EncodeTruncatedToolCallWire())...)
	iu := protoBytesField(7, inner) // InteractionUpdate.partial_tool_call
	return protoBytesField(1, iu)
}

// EncodeAgentToolCallStarted returns AgentServerMessage{tool_call_started}.
func EncodeAgentToolCallStarted(callID string, toolWire []byte) []byte {
	if toolWire == nil {
		toolWire = EncodeTruncatedToolCallWire()
	}
	inner := protoBytesField(1, []byte(callID))
	inner = append(inner, protoBytesField(2, toolWire)...)
	iu := protoBytesField(2, inner) // InteractionUpdate.tool_call_started = 2
	return protoBytesField(1, iu)
}

// EncodeAgentToolCallCompleted returns AgentServerMessage{tool_call_completed}.
func EncodeAgentToolCallCompleted(callID string, toolWire []byte) []byte {
	if toolWire == nil {
		toolWire = EncodeTruncatedToolCallWire()
	}
	inner := protoBytesField(1, []byte(callID))
	inner = append(inner, protoBytesField(2, toolWire)...)
	iu := protoBytesField(3, inner) // InteractionUpdate.tool_call_completed = 3
	return protoBytesField(1, iu)
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

// WriteRunSSETextResponse writes an Agent RunSSE stream with text only. It
// agrees with the shape of the origin that a person observed, but it has no KV
// data:
//
//	heartbeat, then thinking_delta, then one or more text_delta with
//	token_delta, then turn_ended, then the end of Connect
//
// The Content-Type agrees with a Connect stream: application/connect+proto. The
// frames have no compression, with flags=0. A client says
// Connect-Accept-Encoding: gzip, but a frame with no compression is always
// correct.
//
// Facts from the RESP data of ping-glider-5: the large frames at the start are
// KvServerMessage, which is field 4. They are not conversation_checkpoint,
// which is field 3. Field 4 of InteractionUpdate is thinking_delta, and it
// comes before text_delta. This code omits an empty checkpoint in field 3.
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

// WriteRunSSEToolResponse writes a RunSSE stream for Path B. That stream can
// have frames for a tool, which are tool_call_started, partial_tool_call and
// tool_call_completed, and it also has the shape with text only. The code
// uses this function when HasRunSSEToolCodec() is true.
//
// The code changes each CompletionChunk.ToolCalls with the shape of OpenAI
// into the equal ToolCall of Cursor, when it knows the name. For example,
// read becomes Read, and grep becomes Grep. For a name that it does not know,
// it uses TruncatedToolCall.
//
// The code also sends the JSON of the args in args_text_delta of
// partial_tool_call, for each client that reads the arguments as a stream.
func WriteRunSSEToolResponse(w http.ResponseWriter, chunks <-chan backend.CompletionChunk) error {
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
	if err := writeEnvelope(w, 0, EncodeAgentThinkingDelta(" ", 1)); err != nil {
		return err
	}
	if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(1)); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	started := map[string][]byte{} // callID → ToolCall wire
	var sentTokens int32
	for chunk := range chunks {
		if chunk.Content != "" {
			if err := writeEnvelope(w, 0, EncodeAgentTextDelta(chunk.Content)); err != nil {
				return err
			}
			approx := int32(len([]rune(chunk.Content))/4 + 1)
			sentTokens += approx
			if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(approx)); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		for _, tc := range chunk.ToolCalls {
			callID := strings.TrimSpace(tc.ID)
			if callID == "" {
				callID = "call_" + itoaIndex(tc.Index)
			}
			args := ""
			name := ""
			if tc.Function != nil {
				args = tc.Function.Arguments
				name = tc.Function.Name
			}
			toolWire := EncodeMappedToolCallWire(name, args, callID)
			if _, seen := started[callID]; !seen {
				started[callID] = toolWire
				if err := writeEnvelope(w, 0, EncodeAgentToolCallStarted(callID, toolWire)); err != nil {
					return err
				}
			} else if args != "" {
				// Prefer richer mapped wire once full args arrive.
				started[callID] = toolWire
			}
			delta := args
			if name != "" && args == "" {
				delta = `{"name":"` + name + `"}`
			}
			if delta != "" {
				if err := writeEnvelope(w, 0, EncodeAgentPartialToolCall(callID, delta)); err != nil {
					return err
				}
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if chunk.FinishReason == "tool_calls" {
			for callID, toolWire := range started {
				if err := writeEnvelope(w, 0, EncodeAgentToolCallCompleted(callID, toolWire)); err != nil {
					return err
				}
			}
			started = nil
			if flusher != nil {
				flusher.Flush()
			}
			break
		}
		if chunk.FinishReason != "" && chunk.Content == "" && len(chunk.ToolCalls) == 0 {
			break
		}
	}
	// Complete any started tools that never got finish_reason=tool_calls.
	for callID, toolWire := range started {
		if err := writeEnvelope(w, 0, EncodeAgentToolCallCompleted(callID, toolWire)); err != nil {
			return err
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

// WriteRunSSEResponse selects the codec with text only, or the codec that
// knows about tools. It prefers the writer for tools when the flag for the
// codec is on, because that writer also processes parts with text only. Path A
// does not change.
func WriteRunSSEResponse(w http.ResponseWriter, chunks <-chan backend.CompletionChunk) error {
	if HasRunSSEToolCodec() {
		return WriteRunSSEToolResponse(w, chunks)
	}
	return WriteRunSSETextResponse(w, chunks)
}

func itoaIndex(i int) string {
	if i < 0 {
		i = 0
	}
	const digits = "0123456789"
	if i < 10 {
		return string(digits[i])
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b[pos:])
}

// WriteDelegateReplyWithKeepAlive writes the header immediately, as an early
// text_delta. It writes it after the usual start with a heartbeat and a
// thinking_delta, which WriteRunSSETextResponse also sends. Then it waits for a
// value on resultText, and it sends one more heartbeat at each
// keepAliveInterval while it waits.
//
// This exists because of a true defect, and a live test confirmed it on
// 2026-07-29. The HTTP/2 client of cursor-agent stopped a stream with a
// delegate reply, and it gave "http2: stream closed". That stream received zero
// bytes for the full time of a slow delegate call.
//
// A run of the CLI of a different vendor, with no console, can need much more
// time than a usual completion. It has no limit by default. Refer to
// vendors.RunTimeout.
//
// Before this correction, the code found the full text of the reply *before* it
// called WriteReply. Therefore the pattern below, which sends a heartbeat
// first, could not operate early.
//
// The caller, which is DelegateHandler, now finds the reply in a separate
// goroutine. The periodic heartbeats of this function then show that the stream
// is alive for the full wait, and not only at its first moment.
func WriteDelegateReplyWithKeepAlive(w http.ResponseWriter, header string, resultText <-chan string, keepAliveInterval time.Duration) error {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	if err := writeEnvelope(w, 0, EncodeAgentHeartbeat()); err != nil {
		return fmt.Errorf("keepalive preamble heartbeat: %w", err)
	}
	if err := writeEnvelope(w, 0, EncodeAgentThinkingDelta(" ", 1)); err != nil {
		return fmt.Errorf("keepalive preamble thinking_delta: %w", err)
	}
	// A token_delta comes after thinking_delta, and after each text_delta below.
	// That agrees exactly with the shape that WriteRunSSETextResponse uses. That
	// token_delta was absent here at the start, and live testing on 2026-07-29
	// found this. Its absence agrees with the behaviour of the true client of
	// cursor-agent: that client stopped the exchange, also when each write
	// succeeded. Its state machine for RunSSE appears to follow the progress
	// with token_delta, and not with text_delta and turn_ended alone.
	if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(1)); err != nil {
		return fmt.Errorf("keepalive preamble token_delta: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	if header != "" {
		if err := writeEnvelope(w, 0, EncodeAgentTextDelta(header)); err != nil {
			return fmt.Errorf("keepalive header text_delta: %w", err)
		}
		if err := writeEnvelope(w, 0, EncodeAgentTokenDelta(approxTokens(header))); err != nil {
			return fmt.Errorf("keepalive header token_delta: %w", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	if keepAliveInterval <= 0 {
		keepAliveInterval = 10 * time.Second
	}
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	var finalText string
waitLoop:
	for {
		select {
		case text, ok := <-resultText:
			if ok {
				finalText = text
			}
			break waitLoop
		case <-ticker.C:
			if err := writeEnvelope(w, 0, EncodeAgentHeartbeat()); err != nil {
				return fmt.Errorf("keepalive ticker heartbeat: %w", err)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// The code writes finalText, turn_ended and the end-stream trailer of
	// Connect in one Write. It does not write three separate items. A live test
	// confirmed a race on 2026-07-29. The client of cursor-agent closes its read
	// side at the moment when it sees turn_ended. Therefore a *separate* write
	// after that, for the end-stream trailer, frequently lost that race. It gave
	// `keepalive end-stream: http2: stream closed`. The content itself had
	// already gone out correctly. But cursor-agent read that sudden close as a
	// failure, and it tried again. One Write call closes the window to
	// effectively zero.
	var tail bytes.Buffer
	if finalText != "" {
		if err := writeEnvelope(&tail, 0, EncodeAgentTextDelta(finalText)); err != nil {
			return fmt.Errorf("keepalive final text_delta: %w", err)
		}
		if err := writeEnvelope(&tail, 0, EncodeAgentTokenDelta(approxTokens(finalText))); err != nil {
			return fmt.Errorf("keepalive final token_delta: %w", err)
		}
	}
	if err := writeEnvelope(&tail, 0, EncodeAgentTurnEnded()); err != nil {
		return fmt.Errorf("keepalive turn_ended: %w", err)
	}
	if err := writeEnvelope(&tail, flagEndStream, []byte("{}")); err != nil {
		return fmt.Errorf("keepalive end-stream: %w", err)
	}
	if _, err := w.Write(tail.Bytes()); err != nil {
		return fmt.Errorf("keepalive combined tail write: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// approxTokens is the same rough, cosmetic (not billed) token-count
// approximation WriteRunSSETextResponse uses for its own token_delta frames.
func approxTokens(s string) int32 {
	return int32(len([]rune(s))/4 + 1)
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
	case 2: // tool_call_started
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
			}
		}
		return runSSEKindToolCallStarted, wire, textHint
	case 3: // tool_call_completed
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
			}
		}
		return runSSEKindToolCallCompleted, wire, textHint
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
	case 7: // partial_tool_call
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
				if textHint == "" {
					textHint = firstStringField(s, 3) // args_text_delta
				}
			}
		}
		return runSSEKindPartialToolCall, wire, textHint
	case 8: // token_delta
		return runSSEKindTokenDelta, wire, ""
	case 13: // heartbeat
		return runSSEKindHeartbeat, wire, ""
	case 14: // turn_ended
		return runSSEKindTurnEnded, wire, ""
	case 15: // tool_call_delta
		if wireType == 2 {
			if s, ok := readLDPayload(rest); ok {
				textHint = firstStringField(s, 1)
			}
		}
		return runSSEKindToolCallDelta, wire, textHint
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

// SetRunSSEToolCodecEnabled toggles Path B child/tool-loop RunSSE fulfill.
// Default is false (safe). Wired from mitm.agent_rpc_tool_codec / env at process start.
func SetRunSSEToolCodecEnabled(enabled bool) {
	runSSEToolCodecEnabled.Store(enabled)
}

// HasRunSSEToolCodec reports whether Path B can fulfill child/tool-loop RunSSE
// (tool_call frames). When false, callers still re-decide via routing.tool_followup
// and emit tool_followup_would_local, then origin passthrough.
func HasRunSSEToolCodec() bool { return runSSEToolCodecEnabled.Load() }

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
