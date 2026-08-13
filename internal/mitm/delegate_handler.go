package mitm

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/ngl"
	"github.com/glider-ai/glider/internal/vendors"
)

// DelegateHandler does the same work as the Messages route in internal/api,
// but for the transparent path with MITM. It uses the same convention of a
// trailing "<prompt> /vendor-name" flag, through
// vendors.ParseDelegateCommand, and the same dynamic registry of vendors. It
// applies them to /v1/messages traffic that Glider intercepts at the level of
// the operating system, and not to gateway traffic that arrives through
// ANTHROPIC_BASE_URL.
//
// A flag controls this, and a host does not. This is on purpose. Refer to the
// discussion of delegation in planning/ngl_and_adapters.md. A rule with no
// condition would also take the Claude Code session of the operator, if that
// session uses the same front CLI. An example of such a rule is "send each
// request for host X to vendor Y". This design prevents that fault, because
// it needs an explicit flag in the message.
type DelegateHandler struct {
	Log *slog.Logger
	// Metrics is optional, and a nil value is correct and quiet. With a value,
	// each delegate call that Glider dispatches makes a true RequestRecord. This
	// is the same mechanism that Interceptor uses for a usual routing decision.
	// Refer to observe in internal/mitm/intercept.go.
	//
	// Added 2026-07-30, after a true gap that no person had found. This handler
	// wrote a slog line, "mitm delegate: routing to vendor", but it gave the
	// dashboard nothing. Therefore a delegate call was not visible in the request
	// log of the Overview page, and not in the division of LOCAL, CLOUD and
	// CANNED. Each other routing result was visible. A person could find a
	// delegate call only in the raw text of the log, and only with the correct
	// log level.
	//
	// The action "delegate" is a new value. It does not use local, cloud,
	// origin_passthrough, canned or error again. A delegate call is none of
	// those, and to put it in one of them would give an incorrect record of the
	// event.
	Metrics *metrics.Collector
}

// TryHandle implements LocalHandler. It takes only the requests that it can
// answer.
//
// ngl.ResolveOriginAdapter does the recognition. The OriginAdapter of each
// registered vendor examines the shape of the host and the path of this
// request. It then says if that shape is the traffic of its own front CLI.
// Therefore this function never compares r.URL.Path or r.Host with the name
// of a vendor.
//
// Corrected 2026-07-27. This function used a fixed test at its entry,
// `r.URL.Path != "/v1/messages"`. That test is correct only when Claude Code
// is the front CLI. The true traffic of cursor-agent and of agy never has
// that shape. Therefore a delegate flag that a person typed in one of those
// two CLIs did nothing, and it gave no message. The test refused the request
// before any code read the flag. Refer to the comment on OriginAdapter in
// internal/ngl/origin.go for the full record of the incident.
func (h *DelegateHandler) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	adapter := ngl.ResolveOriginAdapter(r)
	if adapter == nil {
		return false, nil
	}

	// The contract of mitmSession says: "LocalHandler must not consume body
	// unless it handles." Refer to the call site.
	//
	// A live test broke this contract one time, on 2026-07-26. The code read the
	// body to examine it, and then returned false and did not put r.Body back.
	// Therefore each *usual* request, which is not a delegate request and which
	// continued to the passthrough to the origin, went out with an empty body.
	// That stopped the true API calls for the full session.
	//
	// The correction: read one time, and then always put r.Body back from the
	// copy in the buffer before any other operation. Thus each path that
	// returns, and this includes "this is not for me", leaves the request
	// exactly as each later handler expects it.
	//
	// Use adapter.ReadRequestBody, and not io.ReadAll(r.Body) for each vendor.
	// For most vendors the two are the same. But AgentService/Run of
	// cursor-agent is a true RPC that streams in two directions. Its client
	// continues to send envelopes that keep the connection alive, on the SAME
	// request stream. It does this for a maximum of approximately 30 seconds,
	// and then it closes the stream. io.ReadAll would block this full handler
	// for that period, before one byte of a reply could go out. That period is
	// long enough for the client to stop and reset the stream first. Refer to
	// the comment on OriginAdapter.ReadRequestBody for the full record of the
	// incident from the live test.
	body, err := adapter.ReadRequestBody(r)
	if err != nil {
		return false, fmt.Errorf("mitm delegate: read body: %w", err)
	}
	// Use io.MultiReader with the first bytes from the buffer and then r.Body.
	// This returned on 2026-07-30, after this same investigation changed it two
	// times.
	//
	// To replace r.Body with only the first envelope, with bytes.NewReader
	// alone, is correct for one condition. That condition is where this handler
	// *handles* the delegate. Glider then sends the request to no other
	// position. But it is incorrect for the condition where the request
	// continues to the origin. The true cursor.sh backend then receives only a
	// short request, which the code closed before its end. It does not receive
	// the full exchange in two directions that a true client sends. Then that
	// backend appears to never complete its own response. A live test confirmed
	// this. A relay with a full independent limit of 120 seconds still received
	// only approximately 200 bytes. It reached its limit with no answer.
	//
	// A person used a MultiReader one time before this and then removed it,
	// because it made a block of approximately 30 seconds return. But a
	// different cause made that block: passthroughHTTPS used the same
	// req.Context() as this inbound request. cursor-agent resets its own stream
	// almost immediately, and it does this independently of the speed of the
	// server. Therefore that early self-cancellation stopped the *full* outbound
	// relay. The read that blocks was one part of that relay.
	//
	// passthroughHTTPS now gives the outbound part its own independent context.
	// Refer to the comment on that function. Therefore it is safe to relay the
	// live stream: a block on the next keepalive of the client no longer stops
	// the relay early. It only continues at the speed of the client, as a usual
	// relay in two directions must.
	//
	// This does nothing for claude and agy. Their ReadRequestBody already reads
	// r.Body to its end with io.ReadAll.
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

	// Code processes a trailing "/workspace" flag before the delegate flag of a
	// vendor. That flag belongs to no vendor. Refer to the comment on
	// ParseWorkspaceCommand. It is of use only when the code can identify the
	// origin process, because the PID is its key.
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

	// Record this turn for the continuity of the session BEFORE the code decides
	// if it is a delegate. The full objective is to collect the turns that
	// Glider sees one at a time. Most of those turns are usual traffic with no
	// delegate. This is what gives a record to a delegate with cursor-agent at
	// the front. The protocol of cursor-agent cannot give that record. Refer to
	// PriorUserInstructions. But Glider observed each turn. Best-effort; a
	// continuity failure must never affect the request the user actually made.
	vendor, templateName, prompt, ok := vendors.ParseDelegateCommand(reg, userText)
	continuityText := userText
	if ok {
		continuityText = prompt // store the task, not the "/vendor" flag noise
	}
	if ws, found := vendors.WorkspaceForPID(originPID); found {
		_ = vendors.RecordContinuity(ws, adapter.Vendor(), originPID, continuityText)
		// Compact in the background once the record outgrows what a delegate
		// could be given. Off this path's clock by construction — see
		// internal/vendors/summarize.go for why that is what makes an
		// origin-CLI summarizer affordable.
		vendors.MaybeCompactContinuity(ws)
	}

	if !ok {
		return false, nil // no delegate flag present — real origin answers as normal
	}

	h.logInfo("mitm delegate: routing to vendor", "front", adapter.Vendor(), "vendor", vendor.Name, "template", templateName, "host", r.Host, "originPID", originPID)

	// The header names the delegate at the start, for two causes. First, the
	// person who reads the reply then knows that a *different* CLI made it. The
	// comment on WriteReply gives the cause: a reply that Glider makes gives no
	// other sign of this. Second, a write at that moment keeps a client with a
	// time limit on an idle stream from stopping. cursor-agent is such a client,
	// and a live test confirmed this on 2026-07-29. That client would stop while
	// ResolveDelegate still operates with no console, in the goroutine below.
	// That call has no limit by default. Refer to vendors.RunTimeout.
	header := fmt.Sprintf("Delegated to %s:\n\n", vendor.Name)

	// Session context for the delegate, so it is not a cold start. This code is
	// here, because this is the position where the intercepted conversation
	// exists. ExtractUserInstruction discards it some lines above. That function
	// returns only the newest message of the person, and it does this on
	// purpose. History extraction goes through the origin adapter
	// (vendor-shaped), never a branch on the front's name. See
	// vendors.ContextPack for how this reaches the delegate: its own context
	// file, not the prompt.
	recent := adapter.PriorUserInstructions(body, vendors.DefaultContextTurns)
	if len(recent) == 0 {
		// This front's protocol carries no retrievable history (cursor-agent
		// — an honest structural limit, not a gap). Glider's own accumulated
		// record covers it, so context quality does not depend on which CLI
		// the human happens to be driving.
		if ws, found := vendors.WorkspaceForPID(originPID); found {
			// Ranked against this delegate's own task, not taken by recency
			// alone: a session that moved between topics should hand over
			// the entries about THIS one.
			recent = vendors.ReadContinuityFor(ws, adapter.Vendor(), originPID, prompt, vendors.DefaultContextTurns)
		}
	}
	pack := vendors.ContextPack{
		FrontVendor: adapter.Vendor(),
		RecentTurns: recent,
	}

	replyCh := make(chan string, 1)
	start := time.Now()
	go func() {
		defer close(replyCh)
		reply := vendors.ResolveDelegateWithContext(r.Context(), vendor, templateName, prompt, originPID, pack)
		// Record the outcome under the FRONT's identity, so a later delegate
		// in this workspace knows this run happened and does not redo or
		// undo it. Best-effort, like every other continuity write.
		if ws, found := vendors.WorkspaceForPID(originPID); found {
			_ = vendors.RecordDelegateOutcome(ws, adapter.Vendor(), originPID, vendor.Name, reply)
		}
		h.recordDelegateMetrics(vendor.Name, templateName, r.Host, r.URL.Path, start)
		replyCh <- reply
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

// recordDelegateMetrics gives one RequestRecord for each delegate call that
// Glider dispatches. It uses the same shape as Interceptor.observe, in
// internal/mitm/intercept.go. Therefore a delegate call appears in the same
// request-log table of the Overview page as each other routing result. The
// Action value "delegate" makes it different. Refer to the comment on
// DelegateHandler.Metrics for the cause.
func (h *DelegateHandler) recordDelegateMetrics(vendorName, templateName, host, path string, start time.Time) {
	if h.Metrics == nil {
		return
	}
	h.Metrics.Record(metrics.RequestRecord{
		ID:      fmt.Sprintf("delegate_%d", time.Now().UnixNano()),
		Mode:    "mitm",
		Action:  "delegate",
		Route:   "delegate",
		Model:   vendorName,
		Host:    host,
		Path:    path,
		Rule:    "delegate:" + templateName,
		Latency: time.Since(start),
	})
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
