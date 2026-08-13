package ngl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// claudeDelegateKeepAliveInterval has the same value as
// cursorDelegateKeepAliveInterval, in adapter_cursor_origin.go. Refer to the
// comment on writeClaudeSSE for the cause: the client of claude needs the same
// operation. An earlier version of this file assumed that it does not.
//
// This is a var, and not a const. The cause is the same as for
// vendors.PendingResumeTTL: a test makes it smaller, and it does not wait for a
// true interval of 10 seconds.
var claudeDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(claudeOriginAdapter{})
}

// claudeOriginAdapter recognizes the true traffic of Claude Code, which has the
// shape of the Messages API of Anthropic.
//
// A live test through ANTHROPIC_BASE_URL confirmed this, and a test through
// transparent interception confirmed it also. Refer to
// planning/ngl_and_adapters.md §10.
//
// The code compares the path only, and it does not compare the host. This is on
// purpose. The full function of ANTHROPIC_BASE_URL is to change the host,
// therefore the host is different each time. POST /v1/messages is the one value
// that does not change, in each method that brings this traffic to Glider.
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
	// LastUserInstruction already does the true work, and it also removes the
	// content that the front CLI adds. This adapter is only a cover around that
	// function, with the shape of an OriginAdapter.
	//
	// Therefore the primitives for the wire format of Anthropic in ngl.go stay of
	// use on their own. The gateway route in internal/api/anthropic_messages.go is
	// for Claude only, on purpose, and it calls LastUserInstruction directly and not
	// through this adapter.
	text, err = LastUserInstruction("claude", req.Messages)
	if err != nil {
		return "", "", false, false, nil
	}
	return text, req.Model, req.Stream, true, nil
}

// PriorUserInstructions reads the messages[] array of Anthropic that this
// request already carries. Claude Code sends the full record of the
// conversation with each call. Therefore this is the source of history with the
// most content, of the three vendors.
//
// It gives the work to ngl.PriorUserInstructions. That function applies the same
// filter of ExtractParts and StripScaffold that ExtractUserInstruction
// applies.
func (claudeOriginAdapter) PriorUserInstructions(body []byte, max int) []string {
	var req claudeWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	prior, err := PriorUserInstructions("claude", req.Messages, max)
	if err != nil {
		return nil
	}
	return prior
}

func (claudeOriginAdapter) WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error {
	if stream {
		return writeClaudeSSE(w, model, header, replyText)
	}
	// This is not a stream: the full JSON body is one write. Therefore no code
	// can put the header on the wire before replyText arrives. No person has
	// confirmed a risk of a time limit for the client of Claude here. The client
	// of cursor-agent is different. header is still folded in for the "delegated
	// to whom" attribution.
	text := header + <-replyText
	return writeClaudeJSON(w, model, text)
}

// writeClaudeJSON and writeClaudeSSE make a reply in the wire shape of the
// Messages API of Anthropic. A person moved them here from the delegate
// handler in internal/mitm. That handler had a second copy of the equal
// functions in internal/api/anthropic_messages.go. Therefore the knowledge of
// the wire shape of a vendor is in exactly one position now, and that is the
// purpose of this file.
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

// writeClaudeSSE does three steps. First it sends header immediately, as its
// own text_delta. That puts true bytes on the wire before the code knows
// replyText. Then, while it waits, it sends a true "ping" event of the
// Anthropic wire format at each claudeDelegateKeepAliveInterval. That event
// is `event: ping` and then `data: {"type":"ping"}`. The true streaming API
// of Anthropic sends the same event to keep an idle connection alive, during
// a long turn that uses a tool. Therefore the client of Claude Code already
// expects it and ignores it. Last, when the text arrives, it sends that text
// as a content_block_delta.
//
// This file used to assume no periodic keep-alive was needed here, unlike
// cursor-agent's WriteReply — live-confirmed wrong (2026-07-31): a real
// Claude Code session, delegating to a real cursor-agent target that took its
// ordinary ~17-20s to complete, hit Claude's client-side idle-stream timeout
// and entered its own retry/backoff loop ("Waiting for API response... will
// retry") — the exact failure mode cursor-agent's own keep-alive was built to
// prevent, just on the opposite side of the same delegate call. Any front can
// be slow to hear back; only the target vendor's typical latency differs.
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
