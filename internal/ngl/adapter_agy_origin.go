package ngl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

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
	if !strings.HasSuffix(r.Host, "cloudcode-pa.googleapis.com") {
		return false
	}
	return strings.Contains(r.URL.Path, ":streamGenerateContent")
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

func (agyOriginAdapter) WriteReply(w http.ResponseWriter, model, replyText string, stream bool) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	event := map[string]any{
		"response": map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"role":  "model",
						"parts": []map[string]any{{"text": replyText}},
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
	_, err = fmt.Fprintf(w, "data: %s\r\n\r\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}
