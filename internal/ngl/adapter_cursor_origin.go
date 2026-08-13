package ngl

import (
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

// cursorDelegateKeepAliveInterval gives the interval at which WriteReply
// sends one more heartbeat frame, while it waits for a slow reply from a
// delegate. Refer to the comment on cursorrpc.WriteDelegateReplyWithKeepAlive
// for the cause: this is the one vendor that needs a heartbeat, and claude and
// agy do not.
const cursorDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(cursorOriginAdapter{})
}

// cursorOriginAdapter recognizes the true completion-plane traffic of
// cursor-agent, which is the CLI in the terminal.
//
// A live test confirmed this on 2026-07-27, with an isolated capture proxy
// that has true support for HTTP/2, tools/wirecapture. A person then compared
// each field with the agent_v1.proto schema, which this repository already
// examined. Refer to planning/vendor_ref/agent_v1.proto.
//
// This closes a gap that planning/agent_cli_interop.md gives. Earlier research
// found "no explicit AgentService/RunSSE/StreamChat* call" in a capture of
// plain HTTP. The cause: the true completion call of the CLI in the terminal,
// agent.v1.AgentService/Run, always agrees HTTP/2. Therefore each capture that
// does not speak true h2 cannot see it. The capture pass of this file closed
// that gap.
//
// The true request that a person captured, from a run with no console,
// `-p "reply with exactly: CURSORCAP_OK"`:
//
//	POST /agent.v1.AgentService/Run HTTP/2.0
//	Host: agentn.global.api5.cursor.sh
//	Content-Type: application/connect+proto
//	Connect-Protocol-Version: 1
//	<5-byte Connect envelope><AgentClientMessage protobuf>
//
// The path of fields to the prompt of the person. A person decoded it by hand
// from the live capture, and then compared it with the field numbers in
// agent_v1.proto. Each length prefix agreed exactly. This includes the length
// in bytes of the text of the prompt, and the enum value
// AgentMode.AGENT_MODE_AGENT=1 beside it:
//
//	AgentClientMessage.run_request           (field 1) → AgentRunRequest
//	  AgentRunRequest.action                 (field 2) → ConversationAction
//	    ConversationAction.user_message_action (field 1) → UserMessageAction
//	      UserMessageAction.user_message     (field 1) → UserMessage
//	        UserMessage.text                 (field 1) → the prompt of the person, plain UTF-8, with nothing around it
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

// ReadRequestBody reads only the first Connect envelope. It does not read the
// full body with io.ReadAll. A live test found the root cause on 2026-07-29,
// with an isolated trace of the HTTP/2 frames from tools/wirecapture.
// AgentService/Run is a true RPC that streams in two directions. The client
// of cursor-agent keeps the send side of its request stream open for
// approximately 30 seconds. It sends a small envelope to keep the connection
// alive at each 5 seconds. Then it sends END_STREAM. It does this even for
// one turn with no console, from -p. io.ReadAll(r.Body) blocks for that full
// period of approximately 30 seconds, before the server can write one byte.
// The client receives nothing in that period. Therefore it sends
// RST_STREAM(CANCEL) at approximately the moment when it completes its own
// send side. Each write after that fails, whatever its shape and whatever its
// time. See cursorrpc.ReadFirstEnvelope's own doc comment for the full
// incident writeup and why reading only the first envelope is not lossy for
// this call shape.
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
// response does not encode one either.
//
// Blocks on replyText, then writes the complete reply in one shot via the
// pre-2026-07-29 WriteRunSSEResponse/CannedCompletionChunks path — NOT
// cursorrpc.WriteDelegateReplyWithKeepAlive's header-first/keepalive-ticker
// streaming. A person added that streaming method on 2026-07-29. It corrected
// one symptom: the client of cursor-agent stopped a reply stream that
// received zero bytes for a period. That correction was true for that
// symptom. But a live test on 2026-07-31 found that the opposite problem is
// worse for this vendor. The AgentService/Run traffic of cursor-agent arrives
// as a slow sequence of small frames. Glider relays that traffic as a plain
// MITM passthrough, while a delegate subprocess does its own work. Those
// frames can need some minutes to become a complete answer. To send that slow
// sequence back live increases the delay, and it does not hide the delay. A
// person removed that method for cursor-agent only, through this same
// WriteReply interface point, one vendor at a time. claude and agy keep their
// own WriteReply behaviour with no change, which sends the header first and
// then a keepalive. Refer to adapter_claude_origin.go and
// adapter_agy_origin.go. No person confirmed a problem with that behaviour
// for either vendor. cursorrpc.WriteDelegateReplyWithKeepAlive itself is left
// in place (unused here now, still tested) in case this needs revisiting once
// the underlying passthrough-relay slowness is separately root-caused.
// PriorUserInstructions returns nil for cursor-agent — an honest limit, not
// an unimplemented stub. ReadRequestBody reads only the FIRST Connect
// envelope, and this is on purpose. Refer to its comment: a read of more
// blocks for approximately 30 seconds on the keepalive envelopes of the
// client, and that stops the request. That first envelope holds the prompt of
// the current turn. It does not hold the earlier turns. Recovering history
// would mean draining a stream Glider must not drain, so there is genuinely
// nothing to return here.
func (cursorOriginAdapter) PriorUserInstructions(body []byte, max int) []string { return nil }

func (cursorOriginAdapter) WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error {
	text := header + <-replyText
	return cursorrpc.WriteRunSSEResponse(w, cursorrpc.CannedCompletionChunks(text))
}

// findLDField returns the data of the first length-delimited field with the
// number wantField, in b. It is a small reader of the protobuf wire format.
// It uses the same low-level method as the encoders and the decoders that a
// person wrote in internal/cursorrpc. No generated Go types exist for
// agent.v1. Refer to the comment at the top of this file for the cause.
// Non-LD wire types (varint, i32, i64) for other field numbers are skipped
// correctly so a later LD field of interest is still reachable.
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
