package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
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
// (confirmed live via ANTHROPIC_BASE_URL, see planning/ngl_and_adapters.md
// §7). Delegate commands ("<prompt> /vendor-name", flag trailing) use the
// same convention as Glider's existing /local /cloud /fast /heavy routing
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
	// needed" rather than erroring. See planning/ngl_and_adapters.md §0 (the rule) for
	// the history of why this was hardcoded in the first place.
	originVendorName := vendors.ResolveOriginVendorName(r.RemoteAddr, reg)
	userText, err := ngl.LastUserInstruction(originVendorName, req.Messages)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid messages: "+err.Error(), "invalid_request_error")
		return
	}
	originPID := vendors.ResolveOriginPID(r.RemoteAddr)

	// A trailing "/workspace" flag is handled before the vendor-scoped delegate flag
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

	// Record the turn for session continuity, same as the MITM path — see
	// internal/mitm/delegate_handler.go's identical block. Both entry points
	// must accumulate, or history would depend on how the caller reached
	// Glider rather than on what the user actually did.
	continuityText := userText
	if isDelegate {
		continuityText = prompt
	}
	if ws, found := vendors.WorkspaceForPID(originPID); found {
		_ = vendors.RecordContinuity(ws, originVendorName, originPID, continuityText)
	}

	var replyText string
	if isDelegate {
		start := time.Now()
		// Session context for the delegate, exactly as the MITM path builds
		// it (internal/mitm/delegate_handler.go) — a delegate reached through
		// the gateway must not be more of a cold start than one reached
		// through transparent interception. This route is deliberately
		// Anthropic-shaped (see anthropicMessagesRequest's doc comment), so
		// it calls ngl.PriorUserInstructions directly for the same reason it
		// calls LastUserInstruction directly rather than going through
		// claudeOriginAdapter. originVendorName is already resolved from the
		// real origin process above, and "" is a safe value — NGL treats an
		// unrecognized vendor as "strip every known scaffold pattern
		// defensively" rather than "strip nothing."
		prior, _ := ngl.PriorUserInstructions(originVendorName, req.Messages, vendors.DefaultContextTurns)
		if len(prior) == 0 {
			if ws, found := vendors.WorkspaceForPID(originPID); found {
				prior = vendors.ReadContinuity(ws, originVendorName, originPID, vendors.DefaultContextTurns)
			}
		}
		pack := vendors.ContextPack{
			FrontVendor: originVendorName,
			RecentTurns: prior,
		}
		replyText = vendors.ResolveDelegateWithContext(r.Context(), vendor, templateName, prompt, originPID, pack)
		h.recordDelegateMetrics(vendor.Name, templateName, r.Host, r.URL.Path, start)
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

// recordDelegateMetrics is this route's counterpart to
// DelegateHandler.recordDelegateMetrics (internal/mitm/delegate_handler.go)
// — see that method's doc comment for why this exists at all. Kept as a
// near-duplicate rather than a shared helper: the two callers live in
// different packages (api, mitm) with no existing shared dependency
// between them worth introducing just for this one small record call.
func (h *Handlers) recordDelegateMetrics(vendorName, templateName, host, path string, start time.Time) {
	if h.Metrics == nil {
		return
	}
	h.Metrics.Record(metrics.RequestRecord{
		ID:      fmt.Sprintf("delegate_%d", time.Now().UnixNano()),
		Mode:    "gateway",
		Action:  "delegate",
		Route:   "delegate",
		Model:   vendorName,
		Host:    host,
		Path:    path,
		Rule:    "delegate:" + templateName,
		Latency: time.Since(start),
	})
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
