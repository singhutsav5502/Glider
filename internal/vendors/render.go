package vendors

import (
	"strings"
	"sync"

	"github.com/glider-ai/glider/internal/ngl"
)

// ResponseDetailClean and ResponseDetailRaw are the two correct values of the
// response-detail setting.
//
// It is a setting of the Registry, and it is not a setting for each vendor. The
// cause is the same as for DefaultWorkspace: one simple control that a person
// changes on the dashboard, and not a configuration for each vendor or for each
// message. This agrees with the preference of this project for simple defaults,
// and against a control for each possible choice.
const (
	ResponseDetailClean = "clean"
	ResponseDetailRaw   = "raw"
)

var (
	responseDetailMu   sync.Mutex
	responseDetailMode = ResponseDetailClean
)

// SetResponseDetail selects the method that ResolveDelegate uses to show the
// output of a delegate run.
//
// Two positions call it: cmd/glider/main.go at the start, with the value from
// the registry on disk, and the dashboard each time that a person changes the
// setting. Those are the same two call sites as SetDefaultWorkspace.
//
// A value that this code does not know becomes ResponseDetailClean, which is
// the safe default. The function does not do nothing with no message.
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

// renderDelegateReply is the one entry point of ResolveDelegate that changes
// the raw captured text of a run that is complete into the body of the reply.
//
// The code uses the raw text with no change in three conditions: in raw mode,
// when no renderer is registered for vendorName, and when the registered
// renderer cannot find a clean answer in this output. But the code then also
// adds a note that a person can see.
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

// cleanReplyOrEmpty extracts the final answer and gives "" when it cannot,
// instead of the raw text plus an apology. It exists for the refusal path,
// which already has a true message to show the person and must not put
// "couldn't parse" in front of it.
//
// The refusal path used the raw text unconditionally until 2026-08-13. The
// reasoning recorded there was that a run which hit a refusal "stopped
// before a final result event, by definition", so there was nothing to
// extract. A live test disproved the "by definition": cursor-agent was
// refused three shell calls, recovered, and still emitted a proper
// {"type":"result"} event with a readable answer. The person received
// approximately fifteen kilobytes of stream-json before the one paragraph
// that mattered.
//
// The premise holds only for a run that stops hard. It does not hold for a
// vendor that treats a refusal as an ordinary tool failure and finishes its
// turn — which is the usual behaviour, not the exception. So the code now
// asks, and falls back to raw only when the answer truly is not there.
func cleanReplyOrEmpty(vendorName, rawText string) string {
	if ResponseDetail() == ResponseDetailRaw {
		return ""
	}
	renderer := ngl.ResolveDelegateRenderer(vendorName)
	if renderer == nil {
		return ""
	}
	clean, ok := renderer.Render([]byte(rawText))
	if !ok {
		return ""
	}
	return strings.TrimSpace(clean)
}
