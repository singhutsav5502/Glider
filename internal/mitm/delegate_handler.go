package mitm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/glider-ai/glider/internal/ngl"
	"github.com/glider-ai/glider/internal/vendors"
)

// DelegateHandler is the transparent/MITM-path counterpart of
// internal/api's Messages route: same "/vendor-name <prompt>" flag
// convention (vendors.ParseDelegateCommand), same dynamic vendor registry,
// applied to real OS-level-intercepted /v1/messages traffic instead of
// gateway traffic reached via ANTHROPIC_BASE_URL. Deliberately flag-gated,
// not host-gated — see planning/native_glider_orchestration.md's delegation
// discussion: an unconditional "everything to host X -> vendor Y" rule would
// also catch the operator's own Claude Code session if it happens to run
// the same front CLI, which is exactly the kind of self-disruption this
// design avoids by requiring an explicit, deliberate flag in the message.
type DelegateHandler struct {
	Log *slog.Logger
}

// TryHandle implements LocalHandler. Only claims requests it can actually
// answer — anything not on the /v1/messages path, or without a matching
// delegate flag, returns handled=false so the caller falls through to
// whatever else it would otherwise do (origin passthrough, another
// LocalHandler in the chain).
func (h *DelegateHandler) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	if r.URL.Path != "/v1/messages" {
		return false, nil
	}

	// mitmSession's own contract (see its call site): "LocalHandler must not
	// consume body unless it handles." Broken once already, live, on
	// 2026-07-26 — reading the body to inspect it and then returning
	// false without restoring r.Body meant every *normal* (non-delegate)
	// request that fell through to origin passthrough afterward went out
	// with an empty body, breaking real API calls for the whole session.
	// Fix: read once, then always restore r.Body from the buffered copy
	// before doing anything else, so every return path — including "not
	// our concern" — leaves the request exactly as any other handler
	// downstream would expect to find it.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false, fmt.Errorf("mitm delegate: read body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req struct {
		Model    string          `json:"model"`
		Stream   bool            `json:"stream"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false, nil // not a shape we understand — let origin handle it
	}

	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		return false, nil
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		return false, nil
	}

	// Origin vendor identification is generalized per-request (2026-07-26)
	// — resolved from the real OS process on the other end of the
	// connection (vendors.ResolveOriginVendorName), not hardcoded to
	// "claude". "" (unresolvable, or a process matching no registered
	// vendor) is a safe, valid result: ngl.LastUserInstruction already
	// treats an unrecognized vendor name as "no scaffold stripping
	// needed" rather than erroring — see planning/adapter_boundary.md §4
	// for the history of why this was hardcoded in the first place.
	originVendorName := vendors.ResolveOriginVendorName(r.RemoteAddr, reg)
	userText, err := ngl.LastUserInstruction(originVendorName, req.Messages)
	if err != nil {
		return false, nil // not a shape we understand — let origin handle it
	}

	originPID := vendors.ResolveOriginPID(r.RemoteAddr)

	// "/workspace <path>" is handled before the vendor-scoped delegate flag
	// — it's not vendor-specific (see ParseWorkspaceCommand's doc comment)
	// and only makes sense to act on when the origin process is actually
	// identifiable, since it's keyed by PID.
	if path, ok := vendors.ParseWorkspaceCommand(userText); ok {
		if originPID == 0 {
			return false, nil // can't key a workspace to an unresolvable origin — let origin handle it as normal chat
		}
		vendors.SetWorkspaceForPID(originPID, path)
		h.logInfo("mitm delegate: workspace set", "pid", originPID, "dir", path)
		replyText := fmt.Sprintf("Workspace set to %q for this session. Resend your delegate request.", path)
		if req.Stream {
			writeAnthropicSSE(w, req.Model, replyText)
		} else {
			writeAnthropicJSON(w, req.Model, replyText)
		}
		return true, nil
	}

	vendor, templateName, prompt, ok := vendors.ParseDelegateCommand(reg, userText)
	if !ok {
		return false, nil // no delegate flag present — real origin answers as normal
	}

	h.logInfo("mitm delegate: routing to vendor", "vendor", vendor.Name, "template", templateName, "host", r.Host, "originPID", originPID)

	replyText := vendors.ResolveDelegate(r.Context(), vendor, templateName, prompt, originPID)

	if req.Stream {
		writeAnthropicSSE(w, req.Model, replyText)
	} else {
		writeAnthropicJSON(w, req.Model, replyText)
	}
	return true, nil
}

func (h *DelegateHandler) logInfo(msg string, args ...any) {
	if h.Log != nil {
		h.Log.Info(msg, args...)
	}
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

// ChainHandler tries each Handler in order, using the first one that
// claims the request (handled=true or a non-nil error). Lets DelegateHandler
// sit in front of the existing Cursor-focused Interceptor without touching
// that interceptor's own logic at all.
type ChainHandler struct {
	Handlers []LocalHandler
}

func (c *ChainHandler) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	for _, h := range c.Handlers {
		handled, err := h.TryHandle(w, r)
		if handled || err != nil {
			return handled, err
		}
	}
	return false, nil
}
