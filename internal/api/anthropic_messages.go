package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/glider-ai/glider/internal/ngl"
	"github.com/glider-ai/glider/internal/vendors"
)

// anthropicMessagesRequest is the minimal subset of Anthropic's Messages API
// request body this gateway route needs to read. Messages is kept raw
// (rather than typed per-message) so it can be handed straight to
// ngl.LastUserInstruction, which is the actual vendor-aware extraction —
// see internal/ngl for why this matters: a naive "concatenate every
// type=text block in the last user message" pass, without that package's
// scaffold-stripping, was the direct cause of a real live bug (2026-07-26)
// where a front's own auto-injected context hijacked delegate routing.
type anthropicMessagesRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages"`
}

// Messages handles POST /v1/messages — Claude Code's completion-plane route
// (confirmed live via ANTHROPIC_BASE_URL, see planning/native_glider_orchestration.md
// §7). Delegate commands ("/vendor-name <prompt>") use the same flag
// convention as Glider's existing /local /cloud /fast /heavy routing
// commands, matched dynamically against whichever CLIs discovery has
// actually found and the dashboard has left enabled (internal/vendors) —
// never a hardcoded vendor name. See vendors.ParseDelegateCommand's doc
// comment for a real, live-confirmed caveat: typed directly into Claude
// Code's own interactive input, the flag must not be the very first
// characters of the message (Claude Code's own client intercepts an
// unrecognized leading "/word" locally and never sends it at all) — the
// flag works fine anywhere else in the text, including from other fronts.
func (h *Handlers) Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid body: "+err.Error(), "invalid_request_error")
		return
	}

	var req anthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error")
		return
	}

	registryPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "vendor registry unavailable: "+err.Error(), "api_error")
		return
	}
	reg, err := vendors.LoadRegistry(registryPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "vendor registry unreadable: "+err.Error(), "api_error")
		return
	}

	// Origin vendor identification is generalized per-request (2026-07-26)
	// — resolved from the real OS process on the other end of the
	// connection, not hardcoded to "claude" (this route was only reachable
	// via ANTHROPIC_BASE_URL, Claude-Code-specific behavior, until this
	// generalization). "" (unresolvable, or a process matching no
	// registered vendor) is a safe, valid result — ngl.LastUserInstruction
	// already treats an unrecognized vendor name as "no scaffold stripping
	// needed" rather than erroring. See planning/adapter_boundary.md §4 for
	// the history of why this was hardcoded in the first place.
	originVendorName := vendors.ResolveOriginVendorName(r.RemoteAddr, reg)
	userText, err := ngl.LastUserInstruction(originVendorName, req.Messages)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid messages: "+err.Error(), "invalid_request_error")
		return
	}
	originPID := vendors.ResolveOriginPID(r.RemoteAddr)

	// "/workspace <path>" is handled before the vendor-scoped delegate flag
	// — see internal/mitm/delegate_handler.go's identical handling for why
	// (not vendor-specific, keyed by the origin process's PID).
	if path, ok := vendors.ParseWorkspaceCommand(userText); ok && originPID != 0 {
		vendors.SetWorkspaceForPID(originPID, path)
		replyText := fmt.Sprintf("Workspace set to %q for this session. Resend your delegate request.", path)
		if req.Stream {
			writeAnthropicSSE(w, req.Model, replyText)
			return
		}
		writeAnthropicJSON(w, req.Model, replyText)
		return
	}

	vendor, templateName, prompt, isDelegate := vendors.ParseDelegateCommand(reg, userText)

	var replyText string
	if isDelegate {
		replyText = vendors.ResolveDelegate(r.Context(), vendor, templateName, prompt, originPID)
	} else if len(reg.Enabled()) == 0 {
		replyText = "No agent CLIs are registered yet. Run discovery from the dashboard's Vendors page " +
			"(or POST /api/vendors/discover) to detect installed CLIs, then address one with \"/name <prompt>\"."
	} else {
		names := make([]string, 0, len(reg.Enabled()))
		for _, v := range reg.Enabled() {
			names = append(names, v.Name)
		}
		replyText = fmt.Sprintf("This Glider gateway route only understands explicit delegate commands "+
			"(e.g. \"/%s <prompt>\"). Registered CLIs: %s.", names[0], joinNames(names))
	}

	if req.Stream {
		writeAnthropicSSE(w, req.Model, replyText)
		return
	}
	writeAnthropicJSON(w, req.Model, replyText)
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func writeAnthropicJSON(w http.ResponseWriter, model, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
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

func writeAnthropicSSE(w http.ResponseWriter, model, text string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicJSON(w, model, text)
		return
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
	send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": len(text) / 4},
	})
	send("message_stop", map[string]any{"type": "message_stop"})
}
