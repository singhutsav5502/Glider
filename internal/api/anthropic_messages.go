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

// anthropicMessagesRequest holds the smallest part of the request body of the
// Messages API of Anthropic that this gateway route must read.
//
// Messages stays raw, and the code does not give a type to each message.
// Therefore the code can give it directly to ngl.LastUserInstruction, which
// does the true extraction and knows the vendor.
//
// Refer to internal/ngl for the cause. A simple method joins each block with
// type=text in the last message of the user, and it does not remove the
// content that the front CLI adds. That method was the direct cause of a true
// defect. A live test found it on 2026-07-26: the content that a front CLI
// added automatically took control of the routing of a delegate.
type anthropicMessagesRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages"`
}

// Messages processes POST /v1/messages. That is the completion-plane route of
// Claude Code, and a live test through ANTHROPIC_BASE_URL confirmed it. Refer
// to planning/ngl_and_adapters.md §7.
//
// A delegate command has the form "<prompt> /vendor-name", with the flag at
// the end. It uses the same convention as the routing commands of Glider:
// /local, /cloud, /fast and /heavy. The code compares it dynamically with the
// CLIs that discovery found and that a person left enabled on the dashboard.
// Refer to internal/vendors. The code never has the name of a vendor in it.
//
// Refer to the comment on vendors.ParseDelegateCommand for a true limit that
// a live test confirmed. A person can type the flag in the interactive input
// of Claude Code. There, the flag must not be the first characters of the
// message. The client of Claude Code takes an unknown "/word" at the start,
// and it never sends it. The flag operates at each other position in the
// text, and it operates from the other front CLIs.
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

	// The code identifies the origin vendor for each request. This became general
	// on 2026-07-26. The code finds it from the true process of the operating
	// system at the other end of the connection. The name "claude" is not fixed in
	// the code. Before that change, only ANTHROPIC_BASE_URL reached this route, and
	// that is behaviour of Claude Code only.
	//
	// An empty name is a safe and correct result. The code cannot always find the
	// process, and a process can agree with no registered vendor.
	// ngl.LastUserInstruction already reads a vendor name that it does not know as
	// "remove no content that a front CLI adds", and it gives no error.
	//
	// Refer to planning/ngl_and_adapters.md §0 for the rule, and for the history of
	// the cause that put the name in the code at the start.
	originVendorName := vendors.ResolveOriginVendorName(r.RemoteAddr, reg)
	userText, err := ngl.LastUserInstruction(originVendorName, req.Messages)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid messages: "+err.Error(), "invalid_request_error")
		return
	}
	originPID := vendors.ResolveOriginPID(r.RemoteAddr)

	// Code processes a trailing "/workspace" flag before the delegate flag of a
	// vendor. Refer to the same code in internal/mitm/delegate_handler.go for
	// the cause. That flag belongs to no vendor, and the PID of the origin
	// process is its key.
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
		// Compact in the background if the record has outgrown what a
		// delegate could ever be given. Never on this path's clock — the
		// request the user made has already been served by the time this
		// matters. See internal/vendors/summarize.go.
		vendors.MaybeCompactContinuity(ws)
	}

	var replyText string
	if isDelegate {
		start := time.Now()
		// This code makes the session context for the delegate, exactly as the MITM
		// path makes it in internal/mitm/delegate_handler.go. A delegate that a
		// person reaches through the gateway must not start more cold than a
		// delegate that a person reaches through transparent interception.
		//
		// This route has the shape of Anthropic, and this is on purpose. Refer to
		// the comment on anthropicMessagesRequest. Therefore it calls
		// ngl.PriorUserInstructions directly, for the same cause that it calls
		// LastUserInstruction directly and does not use claudeOriginAdapter.
		//
		// The code found originVendorName above, from the true origin process. An
		// empty value is safe. NGL reads a vendor that it does not know as "remove
		// each pattern of added content that you know, for protection". It does not
		// read it as "remove nothing".
		prior, _ := ngl.PriorUserInstructions(originVendorName, req.Messages, vendors.DefaultContextTurns)
		if len(prior) == 0 {
			if ws, found := vendors.WorkspaceForPID(originPID); found {
				// Use ReadContinuityFor, and not ReadContinuity. The task of the delegate
				// ranks the record. Therefore a session that moved across several
				// subjects gives the entries about THIS subject, and not the newest
				// entries.
				prior = vendors.ReadContinuityFor(ws, originVendorName, originPID, prompt, vendors.DefaultContextTurns)
			}
		}
		pack := vendors.ContextPack{
			FrontVendor: originVendorName,
			RecentTurns: prior,
		}
		replyText = vendors.ResolveDelegateWithContext(r.Context(), vendor, templateName, prompt, originPID, pack)
		// Record what the delegate did, under the FRONT's identity. Without
		// this a later delegate in the same workspace cannot know this run
		// happened, and may redo or undo it.
		if ws, found := vendors.WorkspaceForPID(originPID); found {
			_ = vendors.RecordDelegateOutcome(ws, originVendorName, originPID, vendor.Name, replyText)
		}
		h.recordDelegateMetrics(vendor.Name, templateName, r.Host, r.URL.Path, start)
	} else if len(reg.Enabled()) == 0 {
		// The example must put the flag LAST. ParseDelegateCommand only reads a
		// trailing flag, and TestParseDelegateCommand_RequiresTrailingPosition
		// pins that. A leading "/name" example told the user to type the one
		// form that cannot work.
		replyText = "No agent CLIs are registered yet. Run discovery from the dashboard's Vendors page " +
			"(or POST /api/vendors/discover) to detect installed CLIs, then address one with \"<prompt> /name\"."
	} else {
		names := make([]string, 0, len(reg.Enabled()))
		for _, v := range reg.Enabled() {
			names = append(names, v.Name)
		}
		replyText = fmt.Sprintf("This Glider gateway route only understands explicit delegate commands "+
			"(e.g. \"<prompt> /%s\"). Registered CLIs: %s.", names[0], joinNames(names))
	}

	if req.Stream {
		writeAnthropicSSE(w, req.Model, replyText)
		return
	}
	writeAnthropicJSON(w, req.Model, replyText)
}

// recordDelegateMetrics is this route's counterpart to
// DelegateHandler.recordDelegateMetrics (internal/mitm/delegate_handler.go) —
// see that method's doc comment for why this exists at all. This code is
// almost the same as its equivalent, and it is not a shared function. The two
// callers are in different packages, api and mitm. Those packages share no
// dependency now. To add one, for this one small call, is not of sufficient
// value.
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
