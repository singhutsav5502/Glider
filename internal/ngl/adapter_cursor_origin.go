package ngl

import (
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

// cursorDelegateKeepAliveInterval is how often WriteReply sends an extra
// heartbeat frame while waiting on a slow delegate reply — see
// cursorrpc.WriteDelegateReplyWithKeepAlive's doc comment for why this is
// the vendor that actually needs one (unlike claude/agy).
const cursorDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(cursorOriginAdapter{})
}

// cursorOriginAdapter recognizes cursor-agent's (the terminal CLI's) own
// real completion-plane traffic — confirmed live 2026-07-27 via an
// isolated capture proxy with genuine HTTP/2 support (tools/wirecapture),
// cross-verified field-for-field against the public agent_v1.proto schema
// already vetted in this codebase (planning/vendor_ref/agent_v1.proto,
// sourced from github.com/burpheart/cursor-tap — see
// internal/cursorrpc/THIRD_PARTY.md). This resolves a gap flagged in
// planning/agent_cli_interop.md: earlier research found "no explicit
// AgentService/RunSSE/StreamChat* call" in a plain-HTTP capture, because
// the terminal CLI's actual completion call, agent.v1.AgentService/Run,
// negotiates HTTP/2 unconditionally — invisible to any capture that
// doesn't speak real h2 (the exact gap this file's capture pass closed).
//
// Real captured request (headless `-p "reply with exactly: CURSORCAP_OK"`):
//
//	POST /agent.v1.AgentService/Run HTTP/2.0
//	Host: agentn.global.api5.cursor.sh
//	Content-Type: application/connect+proto
//	Connect-Protocol-Version: 1
//	<5-byte Connect envelope><AgentClientMessage protobuf>
//
// Field path to the human prompt, hand-decoded from the live capture and
// independently cross-checked against agent_v1.proto's field numbers —
// every length prefix matched exactly, including the prompt string's own
// byte length and the AgentMode.AGENT_MODE_AGENT=1 enum value found
// alongside it:
//
//	AgentClientMessage.run_request           (field 1) → AgentRunRequest
//	  AgentRunRequest.action                 (field 2) → ConversationAction
//	    ConversationAction.user_message_action (field 1) → UserMessageAction
//	      UserMessageAction.user_message     (field 1) → UserMessage
//	        UserMessage.text                 (field 1) → the human's prompt, plain UTF-8, unwrapped
//
// The response side reuses cursorrpc.WriteRunSSEResponse unmodified: Run
// and RunSSE both return `stream AgentServerMessage` per agent_v1.proto —
// the identical message type — and that encoder is already a live,
// confirmed-accurate (not R&D-guessed) part of this codebase's existing
// Path B fulfillment path, not something newly written for this adapter.
type cursorOriginAdapter struct{}

func (cursorOriginAdapter) Vendor() string { return "cursor-agent" }

func (cursorOriginAdapter) Matches(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if !strings.HasSuffix(HostWithoutPort(r), ".cursor.sh") {
		return false
	}
	return r.URL.Path == "/agent.v1.AgentService/Run"
}

// ReadRequestBody reads only the first Connect envelope, not the whole
// body via io.ReadAll — real, live-confirmed root cause (2026-07-29, via
// an isolated tools/wirecapture HTTP/2 frame trace): AgentService/Run is a
// genuine bidi-streaming RPC, and cursor-agent's real client keeps its
// request stream's send side open for roughly 30 seconds — sending a
// small periodic keepalive envelope every ~5s — before finally sending
// END_STREAM, even for one headless -p turn. io.ReadAll(r.Body) blocks
// for that entire ~30s window before the server can write back a single
// byte, and the client, having received nothing at all by the time it
// finishes its own send side, fires RST_STREAM(CANCEL) at essentially
// that same moment — dooming every subsequent write regardless of its
// shape or timing. See cursorrpc.ReadFirstEnvelope's own doc comment for
// the full incident writeup and why reading only the first envelope is
// not lossy for this call shape.
func (cursorOriginAdapter) ReadRequestBody(r *http.Request) ([]byte, error) {
	return cursorrpc.ReadFirstEnvelope(r.Body)
}

func (cursorOriginAdapter) ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error) {
	payload, uerr := cursorrpc.UnwrapProtoPayload(body)
	if uerr != nil {
		return "", "", false, false, nil // not a shape we understand — let origin handle it
	}
	runRequest, found := findLDField(payload, 1) // AgentClientMessage.run_request
	if !found {
		return "", "", false, false, nil
	}
	action, found := findLDField(runRequest, 2) // AgentRunRequest.action
	if !found {
		return "", "", false, false, nil
	}
	userMessageAction, found := findLDField(action, 1) // ConversationAction.user_message_action
	if !found {
		return "", "", false, false, nil
	}
	userMessage, found := findLDField(userMessageAction, 1) // UserMessageAction.user_message
	if !found {
		return "", "", false, false, nil
	}
	promptText, found := findLDField(userMessage, 1) // UserMessage.text
	if !found || len(promptText) == 0 {
		return "", "", false, false, nil
	}
	return string(promptText), "", true, true, nil
}

// WriteReply ignores model — AgentServerMessage's own frames (text_delta,
// token_delta, turn_ended, ...) carry no model field; the real origin
// response doesn't encode one either.
//
// Blocks on replyText, then writes the complete reply in one shot via the
// pre-2026-07-29 WriteRunSSEResponse/CannedCompletionChunks path — NOT
// cursorrpc.WriteDelegateReplyWithKeepAlive's header-first/keepalive-ticker
// streaming. That streaming approach was added 2026-07-29 to fix cursor-
// agent's client abandoning a reply stream that received zero bytes for a
// while — a real fix for that specific symptom — but live testing
// 2026-07-31 found the opposite problem is worse in practice for this
// vendor: cursor-agent's own AgentService/Run traffic (relayed as plain
// MITM passthrough while a delegate subprocess does its own real work)
// arrives as a slow trickle of small frames that can take multiple
// minutes to become a complete answer, and streaming that trickle back
// live compounds the delay rather than hiding it. Reverted specifically
// for cursor-agent, vendor-by-vendor through this same WriteReply
// interface point — claude and agy keep their own header-first/keepalive
// WriteReply behavior unchanged (adapter_claude_origin.go,
// adapter_agy_origin.go), since neither has a confirmed problem with it.
// cursorrpc.WriteDelegateReplyWithKeepAlive itself is left in place
// (unused here now, still tested) in case this needs revisiting once the
// underlying passthrough-relay slowness is separately root-caused.
func (cursorOriginAdapter) WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error {
	text := header + <-replyText
	return cursorrpc.WriteRunSSEResponse(w, cursorrpc.CannedCompletionChunks(text))
}

// findLDField returns the first length-delimited field wantField's payload
// in b — a minimal protobuf wire-format walker matching the same
// low-level approach internal/cursorrpc's own hand-rolled encoders/
// decoders use (no generated Go types exist for agent.v1 — see this
// file's own doc comment for why). Non-LD wire types (varint, i32, i64)
// for other field numbers are skipped correctly so a later LD field of
// interest is still reachable.
func findLDField(b []byte, wantField int) ([]byte, bool) {
	off := 0
	for off < len(b) {
		key, n := readVarintLocal(b[off:])
		if n <= 0 {
			return nil, false
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		switch wireType {
		case 0: // varint
			_, vn := readVarintLocal(b[off:])
			if vn <= 0 {
				return nil, false
			}
			off += vn
		case 1: // 64-bit
			if off+8 > len(b) {
				return nil, false
			}
			off += 8
		case 2: // length-delimited
			ln, vn := readVarintLocal(b[off:])
			if vn <= 0 || off+vn+int(ln) > len(b) {
				return nil, false
			}
			off += vn
			chunk := b[off : off+int(ln)]
			off += int(ln)
			if fieldNum == wantField {
				return chunk, true
			}
		case 5: // 32-bit
			if off+4 > len(b) {
				return nil, false
			}
			off += 4
		default:
			return nil, false
		}
	}
	return nil, false
}

func readVarintLocal(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}
