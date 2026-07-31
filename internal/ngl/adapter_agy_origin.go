package ngl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// agyDelegateKeepAliveInterval mirrors cursorDelegateKeepAliveInterval
// (adapter_cursor_origin.go) — see WriteReply's own doc comment for why
// agy's own client needs the identical treatment, contrary to what this
// file used to assume. A var, not a const — same reason as
// vendors.PendingResumeTTL: tests shrink it rather than waiting out a real
// 10s interval.
var agyDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(agyOriginAdapter{})
}

// agyOriginAdapter recognizes agy's own real traffic — confirmed live
// 2026-07-27 via an isolated capture proxy (tools/wirecapture), NOT
// inferred from documentation or reused from another vendor's shape. Real
// captured request:
//
//	POST https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent
//	Authorization: Bearer ya29...  (OAuth2, not an API key)
//	{"project":"...", "requestId":"...",
//	 "request":{"contents":[{"role":"user","parts":[{"text":
//	   "<USER_REQUEST>\n<the human's actual text>\n</USER_REQUEST>\n<ADDITIONAL_METADATA>...</ADDITIONAL_METADATA>"
//	 }]}], "systemInstruction":{...}, "tools":[...], "generationConfig":{...}, "sessionId":"..."},
//	 "model":"gemini-3.6-flash-high", "userAgent":"antigravity", "requestType":"agent"}
//
// Real captured response (SSE, event boundaries are "\r\n\r\n" not "\n\n"):
//
//	data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"..."}]}}],
//	  "usageMetadata":{...},"modelVersion":"gemini-3.6-flash","responseId":"..."},
//	  "traceId":"...","metadata":{}}
//
// This is a Gemini-Cloud-Code-Assist-internal shape, structurally
// unrelated to both Anthropic's Messages API and Cursor's Connect-RPC
// family — confirms per-vendor OriginAdapters are load-bearing, not
// ceremony: none of the three vendors captured so far share a wire shape.
type agyOriginAdapter struct{}

func (agyOriginAdapter) Vendor() string { return "agy" }

func (agyOriginAdapter) Matches(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if !strings.HasSuffix(HostWithoutPort(r), "cloudcode-pa.googleapis.com") {
		return false
	}
	return strings.Contains(r.URL.Path, ":streamGenerateContent")
}

// ReadRequestBody: plain request/response, no bidi-streaming concern — see
// OriginAdapter.ReadRequestBody's doc comment for why this differs from
// cursorOriginAdapter's.
func (agyOriginAdapter) ReadRequestBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(r.Body)
}

type agyWireRequest struct {
	Request struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	} `json:"request"`
	Model string `json:"model"`
}

// agyUserRequestTag matches agy's own auto-injected wrapper around genuine
// human input — the direct agy-side analogue of ngl.go's Claude
// <system-reminder> stripper, confirmed from the real capture above.
// Unlike Claude's convention (strip the scaffold, keep the surrounding
// text), agy's convention is inverted: the ENTIRE parts[].text blob is
// agy's own construction, with the genuine human text nested inside
// <USER_REQUEST>...</USER_REQUEST> and sibling tags
// (<ADDITIONAL_METADATA>, <USER_SETTINGS_CHANGE>, ...) holding agy's own
// injected context alongside it — so extraction here means pulling the
// tag's inner content out, not removing a wrapper from around real text.
var agyUserRequestTag = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)

func (agyOriginAdapter) ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error) {
	var req agyWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", "", false, false, nil // not a shape we understand — let origin handle it
	}
	for i := len(req.Request.Contents) - 1; i >= 0; i-- {
		c := req.Request.Contents[i]
		if c.Role != "user" {
			continue
		}
		var raw strings.Builder
		for _, p := range c.Parts {
			raw.WriteString(p.Text)
		}
		m := agyUserRequestTag.FindStringSubmatch(raw.String())
		if m == nil {
			// Structurally a user turn, but not wrapped in agy's known
			// <USER_REQUEST> convention — refuse rather than guess (see
			// OriginAdapter.ExtractUserInstruction's doc comment).
			return "", "", false, false, nil
		}
		return strings.TrimSpace(m[1]), req.Model, true, true, nil
	}
	return "", "", false, false, nil
}

// PriorUserInstructions walks agy's own request.contents[] array, which
// carries prior turns. Each user turn's text is unwrapped from agy's
// <USER_REQUEST> convention the same way ExtractUserInstruction does —
// without that, the returned "history" would be agy's own injected
// metadata rather than anything a human typed.
func (agyOriginAdapter) PriorUserInstructions(body []byte, max int) []string {
	if max <= 0 {
		return nil
	}
	var req agyWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	var collected []string
	seenLatest := false
	for i := len(req.Request.Contents) - 1; i >= 0 && len(collected) < max; i-- {
		c := req.Request.Contents[i]
		if c.Role != "user" {
			continue
		}
		var raw strings.Builder
		for _, part := range c.Parts {
			raw.WriteString(part.Text)
		}
		m := agyUserRequestTag.FindStringSubmatch(raw.String())
		if m == nil {
			continue // not agy's human-input convention — never guess
		}
		if !seenLatest {
			seenLatest = true // the delegate's own task, restated elsewhere
			continue
		}
		if text := strings.TrimSpace(m[1]); text != "" {
			collected = append(collected, text)
		}
	}
	for l, r := 0, len(collected)-1; l < r; l, r = l+1, r-1 {
		collected[l], collected[r] = collected[r], collected[l]
	}
	return collected
}

// WriteReply sends header as its own SSE data: event immediately (real
// bytes on the wire before replyText is known), then sends an empty-text
// data: event (same wire shape, just parts:[{"text":""}] — a harmless
// no-op append for a client accumulating candidates[].content.parts[].text)
// every agyDelegateKeepAliveInterval while waiting, before finally sending
// the resolved text as a last data: event once it arrives. agy's endpoint
// is itself named streamGenerateContent, so a real backend emitting more
// than one frame over the connection's lifetime is consistent with its
// own protocol, even though the only live capture this codebase has on
// file (see this file's own doc comment) happened to be a single short
// frame.
//
// This file used to assume no periodic keep-alive was needed here, unlike
// cursor-agent's WriteReply — live-confirmed wrong (2026-07-31) via the
// same incident that fixed claudeOriginAdapter's identical assumption
// (see writeClaudeSSE's doc comment, adapter_claude_origin.go): a real agy
// session delegating to a real cursor-agent target sat waiting with no
// keep-alive for the same reason claude's did.
func (agyOriginAdapter) WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	sendEvent := func(text string) error {
		event := map[string]any{
			"response": map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role":  "model",
							"parts": []map[string]any{{"text": text}},
						},
					},
				},
				"usageMetadata": map[string]any{
					"promptTokenCount":     0,
					"candidatesTokenCount": 0,
					"totalTokenCount":      0,
				},
				"modelVersion": model,
				"responseId":   "glider_delegate",
			},
			"traceId":  "glider_delegate",
			"metadata": map[string]any{},
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		// "\r\n\r\n" event boundary matches the real captured response byte
		// for byte — agy's own client parser was built against that framing.
		if _, err := fmt.Fprintf(w, "data: %s\r\n\r\n", payload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	if header != "" {
		if err := sendEvent(header); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(agyDelegateKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case text, ok := <-replyText:
			if !ok {
				text = ""
			}
			return sendEvent(text)
		case <-ticker.C:
			if err := sendEvent(""); err != nil {
				return err
			}
		}
	}
}
