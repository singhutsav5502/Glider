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
	//
	// adapter.ReadRequestBody, not a blanket io.ReadAll(r.Body): for most
	// vendors these are identical, but cursor-agent's AgentService/Run is
	// a genuine bidi-streaming RPC whose real client keeps sending
	// periodic keepalive envelopes on the SAME request stream for up to
	// ~30s before actually closing it — io.ReadAll would block this whole
	// handler for that entire window before a single reply byte could go
	// out, long enough for the client to give up and reset the stream
	// first. See OriginAdapter.ReadRequestBody's doc comment for the full,
	// live-confirmed incident writeup.
	body, err := adapter.ReadRequestBody(r)
	if err != nil {
		return false, fmt.Errorf("mitm delegate: read body: %w", err)
	}
	// io.MultiReader(buffered-first-bytes, r.Body), restored 2026-07-30
	// after a real back-and-forth this same investigation: replacing
	// r.Body with only the first envelope (bytes.NewReader alone) is
	// right for the *handled* delegate case (Glider never forwards the
	// request anywhere), but wrong for the fall-through-to-origin case —
	// the real cursor.sh backend, when relayed only a truncated,
	// artificially-closed request instead of the full ongoing bidi
	// exchange a real client sends, appears to simply never finish
	// generating its own response (confirmed live: a relay given a full
	// 120s independent timeout still only received ~200 bytes total and
	// timed out, never the actual answer). A MultiReader was tried once
	// before this and reverted, because it reintroduced a ~30s block —
	// but that block turned out to be caused by passthroughHTTPS sharing
	// req.Context() with this inbound request, so cursor-agent's own
	// early self-cancellation (it resets its own stream almost
	// immediately, independent of server speed) poisoned the *entire*
	// outbound relay, blocking read included. Now that passthroughHTTPS
	// gives the outbound leg its own independent context (see that
	// function's own doc comment), relaying the live stream is safe:
	// blocking on the client's next keepalive no longer aborts early,
	// it just proceeds at the client's own pace like a normal bidi relay
	// should. A no-op for claude/agy, whose ReadRequestBody already
	// drains r.Body to EOF via io.ReadAll.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))

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
		if err := adapter.WriteReply(w, model, stream, "", instantReply(replyText)); err != nil {
			h.logInfo("mitm delegate: write reply failed", "vendor", adapter.Vendor(), "err", err)
		}
		return true, nil
	}

	vendor, templateName, prompt, ok := vendors.ParseDelegateCommand(reg, userText)
	if !ok {
		return false, nil // no delegate flag present — real origin answers as normal
	}

	h.logInfo("mitm delegate: routing to vendor", "front", adapter.Vendor(), "vendor", vendor.Name, "template", templateName, "host", r.Host, "originPID", originPID)

	// header names the delegate up front — both so the human reading the
	// reply knows a *different* CLI actually produced it (WriteReply's own
	// doc comment covers why a synthesized reply otherwise gives no such
	// indication), and because writing it immediately is what keeps a
	// vendor whose client has a stream-idle timeout (cursor-agent,
	// confirmed live 2026-07-29) from giving up while ResolveDelegate is
	// still running headless in the background goroutine below — that
	// call can take up to vendors.RunTimeout (120s).
	header := fmt.Sprintf("Delegated to %s:\n\n", vendor.Name)
	replyCh := make(chan string, 1)
	go func() {
		defer close(replyCh)
		replyCh <- vendors.ResolveDelegate(r.Context(), vendor, templateName, prompt, originPID)
	}()

	if err := adapter.WriteReply(w, model, stream, header, replyCh); err != nil {
		h.logInfo("mitm delegate: write reply failed", "vendor", adapter.Vendor(), "err", err)
	}
	return true, nil
}

// instantReply wraps an already-known reply string as the <-chan string
// shape OriginAdapter.WriteReply expects, for the meta-replies (workspace
// set, ...) that have nothing to wait on.
func instantReply(text string) <-chan string {
	ch := make(chan string, 1)
	ch <- text
	close(ch)
	return ch
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
