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

// agyDelegateKeepAliveInterval has the same value as
// cursorDelegateKeepAliveInterval, in adapter_cursor_origin.go. Refer to the
// comment on WriteReply for the cause: the client of agy needs the same
// operation. An earlier version of this file assumed that it does not.
//
// This is a var, and not a const. The cause is the same as for
// vendors.PendingResumeTTL: a test makes it smaller, and it does not wait for a
// true interval of 10 seconds.
var agyDelegateKeepAliveInterval = 10 * time.Second

func init() {
	RegisterOriginAdapter(agyOriginAdapter{})
}

// agyOriginAdapter recognizes the true traffic of agy. A live test confirmed
// this on 2026-07-27, with an isolated capture proxy, tools/wirecapture. No
// person read it from a document, and no person copied the shape of a
// different vendor.
//
// The true request that a person captured:
//
// 	POST https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent
// 	Authorization: Bearer ya29...  (OAuth2, not an API key)
// 	{"project":"...", "requestId":"...",
// 	 "request":{"contents":[{"role":"user","parts":[{"text":
// 	   "<USER_REQUEST>\n<the human's actual text>\n</USER_REQUEST>\n<ADDITIONAL_METADATA>...</ADDITIONAL_METADATA>"
// 	 }]}], "systemInstruction":{...}, "tools":[...], "generationConfig":{...}, "sessionId":"..."},
// 	 "model":"gemini-3.6-flash-high", "userAgent":"antigravity", "requestType":"agent"}
//
// The true response that a person captured. It is SSE, and the events end
// with "\r\n\r\n" and not with "\n\n":
//
// 	data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"..."}]}}],
// 	  "usageMetadata":{...},"modelVersion":"gemini-3.6-flash","responseId":"..."},
// 	  "traceId":"...","metadata":{}}
//
// This is an internal shape of Gemini Cloud Code Assist. Its structure has no
// relation to the Messages API of Anthropic, and no relation to the
// Connect-RPC family of Cursor.
//
// Therefore an OriginAdapter for each vendor is necessary, and it is not a
// formality. No two of the three vendors that a person captured share a shape
// on the wire.
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

// agyUserRequestTag agrees with the content that agy adds automatically around
// the true input of a person. It is the equivalent for agy of the stripper for
// <system-reminder> of Claude in ngl.go. A person confirmed it from the true
// capture above.
//
// But the convention of agy is the opposite of the convention of Claude. For
// Claude, the code removes the added content and keeps the text around it. For
// agy, the FULL text in parts[].text is the construction of agy. The true text
// of the person is inside <USER_REQUEST>...</USER_REQUEST>, and other tags
// beside it hold the data of agy. Those other tags are <ADDITIONAL_METADATA>,
// <USER_SETTINGS_CHANGE> and others.
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
// carries prior turns. The code removes the <USER_REQUEST> content of agy
// from the text of each turn of the user. It uses the same method as
// ExtractUserInstruction. Without that step, the record that this function
// returns would be the data that agy adds, and not the words of a person.
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

// WriteReply does three steps. First it sends header immediately, as its own
// SSE data: event. That puts true bytes on the wire before the code knows
// replyText. Then, while it waits, it sends an empty data: event at each
// agyDelegateKeepAliveInterval. That event has the same wire shape, with
// parts:[{"text":""}]. It adds nothing, and it does no damage to a client
// that collects candidates[].content.parts[].text. Last, when the text
// arrives, it sends that text as a final data: event. The endpoint of agy has
// the name streamGenerateContent. Therefore a true backend that sends more
// than one frame during the life of the connection agrees with its own
// protocol. The one live capture that this repository has was a single short
// frame. Refer to the comment at the top of this file.
//
// This file assumed before that it needs no periodic keepalive here, and that
// WriteReply of cursor-agent does need one. A live test on 2026-07-31
// confirmed that this assumption is incorrect. The same incident corrected
// the equal assumption in claudeOriginAdapter. Refer to the comment on
// writeClaudeSSE, in adapter_claude_origin.go. A true agy session delegated
// to a true cursor-agent target, and it waited with no keepalive, for the
// same cause as claude.
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
