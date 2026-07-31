package ngl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// claudeDelegateKeepAliveInterval mirrors cursorDelegateKeepAliveInterval
// (adapter_cursor_origin.go) — see writeClaudeSSE's own doc comment for
// why claude's own client needs the identical treatment, contrary to what
// this file used to assume. A var, not a const — same reason as
// vendors.PendingResumeTTL: tests shrink it rather than waiting out a real
// 10s interval.
var claudeDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(claudeOriginAdapter{})
}

// claudeOriginAdapter recognizes Claude Code's own real traffic: the
// Anthropic Messages API shape, confirmed live via ANTHROPIC_BASE_URL
// (planning/ngl_and_adapters.md §10) and via transparent
// interception. Path-only matching (no host check) is deliberate — the
// whole point of ANTHROPIC_BASE_URL repointing is that the host varies,
// while POST /v1/messages is the one constant across every way this
// traffic reaches Glider.
type claudeOriginAdapter struct{}

func (claudeOriginAdapter) Vendor() string { return "claude" }

func (claudeOriginAdapter) Matches(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/messages"
}

// ReadRequestBody: plain request/response, no bidi-streaming concern — see
// OriginAdapter.ReadRequestBody's doc comment for why this differs from
// cursorOriginAdapter's.
func (claudeOriginAdapter) ReadRequestBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(r.Body)
}

type claudeWireRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages"`
}

func (claudeOriginAdapter) ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error) {
	var req claudeWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", "", false, false, nil // not a shape we understand — let origin handle it, not an error
	}
	// LastUserInstruction already does the real work (scaffold-stripping
	// included) — this adapter is just the OriginAdapter-shaped wrapper
	// around it, so the Anthropic-wire-format primitives in ngl.go stay
	// usable on their own (internal/api/anthropic_messages.go, a
	// deliberately Claude-only gateway route, calls LastUserInstruction
	// directly rather than through this adapter).
	text, err = LastUserInstruction("claude", req.Messages)
	if err != nil {
		return "", "", false, false, nil
	}
	return text, req.Model, req.Stream, true, nil
}

func (claudeOriginAdapter) WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error {
	if stream {
		return writeClaudeSSE(w, model, header, replyText)
	}
	// Non-streaming: the whole JSON body is one atomic write, so there is
	// no way to get header on the wire before replyText resolves — no
	// timeout risk has ever been confirmed for Claude's own client here,
	// unlike cursor-agent's. header is still folded in for the "delegated
	// to whom" attribution.
	text := header + <-replyText
	return writeClaudeJSON(w, model, text)
}

// writeClaudeJSON and writeClaudeSSE render a reply in the Anthropic
// Messages API's own wire shape — moved here from internal/mitm's
// delegate handler (which duplicated internal/api/anthropic_messages.go's
// identical functions) so vendor wire-shape knowledge lives in exactly one
// place, per this file's own purpose.
func writeClaudeJSON(w http.ResponseWriter, model, text string) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"id":            "msg_glider_delegate",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]any{{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	})
}

// writeClaudeSSE sends header as its own text_delta immediately (real
// bytes on the wire before replyText is known), then sends a real
// Anthropic-wire "ping" event (event: ping\ndata: {"type":"ping"}\n\n —
// the same idle-keepalive event Anthropic's own real streaming API sends
// during long tool-use turns, so this is what Claude Code's client already
// natively expects and ignores) every claudeDelegateKeepAliveInterval
// while waiting, before finally sending the resolved text as a
// content_block_delta once it arrives.
//
// This file used to assume no periodic keep-alive was needed here, unlike
// cursor-agent's WriteReply — live-confirmed wrong (2026-07-31): a real
// Claude Code session, delegating to a real cursor-agent target that took
// its ordinary ~17-20s to complete, hit Claude's client-side idle-stream
// timeout and entered its own retry/backoff loop ("Waiting for API
// response... will retry") — the exact failure mode cursor-agent's own
// keep-alive was built to prevent, just on the opposite side of the same
// delegate call. Any front can be slow to hear back; only the target
// vendor's typical latency differs.
func writeClaudeSSE(w http.ResponseWriter, model, header string, replyText <-chan string) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		text := header + <-replyText
		return writeClaudeJSON(w, model, text)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(event string, data map[string]any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		flusher.Flush()
	}

	send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_glider_delegate", "type": "message", "role": "assistant",
			"model": model, "content": []any{}, "stop_reason": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
	send("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	if header != "" {
		send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": header},
		})
	}
	var text string
	ticker := time.NewTicker(claudeDelegateKeepAliveInterval)
	defer ticker.Stop()
waitLoop:
	for {
		select {
		case t, ok := <-replyText:
			if ok {
				text = t
			}
			break waitLoop
		case <-ticker.C:
			send("ping", map[string]any{"type": "ping"})
		}
	}
	send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": (len(header) + len(text)) / 4},
	})
	send("message_stop", map[string]any{"type": "message_stop"})
	return nil
}
