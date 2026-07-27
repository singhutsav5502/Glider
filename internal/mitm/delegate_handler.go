package mitm

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/glider-ai/glider/internal/ngl"
	"github.com/glider-ai/glider/internal/vendors"
)

// DelegateHandler is the transparent/MITM-path counterpart of
// internal/api's Messages route: same trailing "<prompt> /vendor-name" flag
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
// answer. Recognition is dispatched through ngl.ResolveOriginAdapter —
// each registered vendor's own OriginAdapter reports whether it recognizes
// this request's host/path shape as its own front-CLI traffic, so this
// function never compares r.URL.Path or r.Host against a literal vendor
// name. Fixed 2026-07-27: this used to hardcode `r.URL.Path != "/v1/messages"`
// as its entry gate, which only happens to be true when Claude Code is the
// front CLI — cursor-agent's and agy's own real traffic is never that
// shape, so typing a delegate flag directly into either of those CLIs
// silently did nothing (the gate rejected the request before any
// flag-parsing ran). See internal/ngl/origin.go's OriginAdapter doc
// comment for the full incident writeup.
func (h *DelegateHandler) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	adapter := ngl.ResolveOriginAdapter(r)
	if adapter == nil {
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

	userText, model, stream, ok, err := adapter.ExtractUserInstruction(body)
	if err != nil {
		return false, fmt.Errorf("mitm delegate: extract instruction: %w", err)
	}
	if !ok {
		return false, nil // recognized front, but no confirmed way to isolate human text — let origin handle it
	}

	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		return false, nil
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		return false, nil
	}

	originPID := vendors.ResolveOriginPID(r.RemoteAddr)

	// A trailing "/workspace" flag is handled before the vendor-scoped delegate flag
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
		if err := adapter.WriteReply(w, model, replyText, stream); err != nil {
			h.logInfo("mitm delegate: write reply failed", "vendor", adapter.Vendor(), "err", err)
		}
		return true, nil
	}

	vendor, templateName, prompt, ok := vendors.ParseDelegateCommand(reg, userText)
	if !ok {
		return false, nil // no delegate flag present — real origin answers as normal
	}

	h.logInfo("mitm delegate: routing to vendor", "front", adapter.Vendor(), "vendor", vendor.Name, "template", templateName, "host", r.Host, "originPID", originPID)

	replyText := vendors.ResolveDelegate(r.Context(), vendor, templateName, prompt, originPID)

	if err := adapter.WriteReply(w, model, replyText, stream); err != nil {
		h.logInfo("mitm delegate: write reply failed", "vendor", adapter.Vendor(), "err", err)
	}
	return true, nil
}

func (h *DelegateHandler) logInfo(msg string, args ...any) {
	if h.Log != nil {
		h.Log.Info(msg, args...)
	}
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
