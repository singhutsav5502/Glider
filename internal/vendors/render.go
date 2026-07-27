package vendors

import (
	"strings"
	"sync"

	"github.com/glider-ai/glider/internal/ngl"
)

// ResponseDetailClean and ResponseDetailRaw are the two valid values for
// the response-detail setting — a Registry-level (not per-vendor) toggle,
// same reasoning as DefaultWorkspace: a single, simple, dashboard-editable
// knob rather than per-vendor or per-message configuration, matching this
// project's stated preference for simple defaults over exposing every
// possible axis of control.
const (
	ResponseDetailClean = "clean"
	ResponseDetailRaw   = "raw"
)

var (
	responseDetailMu   sync.Mutex
	responseDetailMode = ResponseDetailClean
)

// SetResponseDetail configures how ResolveDelegate renders a delegate
// run's output — called once at startup (cmd/glider/main.go, seeded from
// the persisted registry) and again by the dashboard whenever the setting
// changes, same two call sites as SetDefaultWorkspace. An unrecognized
// value is treated as ResponseDetailClean (the safe default) rather than
// silently doing nothing.
func SetResponseDetail(mode string) {
	responseDetailMu.Lock()
	defer responseDetailMu.Unlock()
	if mode == ResponseDetailRaw {
		responseDetailMode = ResponseDetailRaw
		return
	}
	responseDetailMode = ResponseDetailClean
}

// ResponseDetail returns the currently configured mode.
func ResponseDetail() string {
	responseDetailMu.Lock()
	defer responseDetailMu.Unlock()
	return responseDetailMode
}

// renderDelegateReply is ResolveDelegate's single entry point for turning
// a completed run's raw captured text into the reply body. In raw mode, or
// when no renderer is registered for vendorName, or when the registered
// renderer can't find a clean answer in this particular output, the raw
// text is used as-is — but a visible note is appended in the "renderer
// exists but declined" case specifically (not the "no renderer at all"
// case), since only the former means formatting genuinely degraded for
// THIS run rather than simply being unavailable for this vendor at all.
func renderDelegateReply(vendorName, rawText string) string {
	if ResponseDetail() == ResponseDetailRaw {
		return rawText
	}
	renderer := ngl.ResolveDelegateRenderer(vendorName)
	if renderer == nil {
		return rawText
	}
	clean, ok := renderer.Render([]byte(rawText))
	if !ok {
		return rawText + "\n\n(couldn't parse a clean result from " + vendorName + "'s output — showing raw output instead.)"
	}
	return strings.TrimSpace(clean)
}
