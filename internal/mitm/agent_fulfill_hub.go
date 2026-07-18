package mitm

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/router"
)

// Default wait for BidiAppend decision after RunSSE opens (same-second in captures).
// Kept short so idle/prewarm RunSSE reconnects fail-soft to origin quickly.
const defaultRunSSEFulfillWait = 800 * time.Millisecond

// DefaultTurnFamilyTTL is how long a DecideLocal / explicit turn family stays
// open for immediate follow-ons (reply summary, title gen). Not conversation-wide:
// the next real user message re-decides via classifier.
// Renewed while a parent cloud RunSSE is in-flight and on each sticky bind.
const DefaultTurnFamilyTTL = 90 * time.Second

// StickyMode is the Path B preference for one turn family (explicit or DecideLocal).
type StickyMode int

const (
	StickyNone StickyMode = iota
	StickyCloud
	StickyLocal
)

func (m StickyMode) String() string {
	switch m {
	case StickyCloud:
		return "cloud"
	case StickyLocal:
		return "local"
	default:
		return "none"
	}
}

// AgentFulfillOffer is the decision signaled from a context_envelope BidiAppend
// to a waiting (or soon-to-arrive) RunSSE stream for the same request UUID.
type AgentFulfillOffer struct {
	Local    bool
	Request  *backend.CompletionRequest
	Decision *backend.RoutingDecision
	UserText string
	Source   string
}

// AgentFulfillHub correlates BidiAppend prompt extract with RunSSE response hijack.
// Prompt arrives on BidiAppend; answer is written on RunSSE (cursor-tap split).
//
// Turn-family sticky (not conversation-wide): an explicit flag OR a DecideLocal
// cloud|local decision opens a short-lived family keyed by the root request UUID.
// Immediate follow-ons (reply-summary / title / chrome wrap-up) and mid-turn
// children inherit; the next real user message re-decides. Child tool loops
// re-decide via tool_followup.
type AgentFulfillHub struct {
	mu        sync.Mutex
	waiting   map[string]chan *AgentFulfillOffer // requestID → waiter
	pending   map[string]*AgentFulfillOffer      // BidiAppend arrived first
	family    *turnFamily
	ttl       time.Duration
	familyTTL time.Duration
	Graph     *contextgraph.Store // optional; nil → contextgraph.Default()
}

type turnFamily struct {
	Mode          StickyMode
	RootRequestID string
	Until         time.Time
	Source        string
	ParentActive  int // in-flight root RunSSE count; keeps family live past Until
}

// NewAgentFulfillHub constructs a coordination hub.
func NewAgentFulfillHub() *AgentFulfillHub {
	return &AgentFulfillHub{
		waiting:   make(map[string]chan *AgentFulfillOffer),
		pending:   make(map[string]*AgentFulfillOffer),
		ttl:       30 * time.Second,
		familyTTL: DefaultTurnFamilyTTL,
	}
}

var defaultAgentFulfillHub = NewAgentFulfillHub()

func (h *AgentFulfillHub) graph() *contextgraph.Store {
	if h != nil && h.Graph != nil {
		return h.Graph
	}
	return contextgraph.Default()
}

// ArmLocal records a local-intent offer for requestID (from BidiAppend).
// If a RunSSE waiter exists, it is signaled immediately.
func (h *AgentFulfillHub) ArmLocal(requestID string, offer *AgentFulfillOffer) {
	if h == nil || requestID == "" || offer == nil {
		return
	}
	offer.Local = true
	h.signal(requestID, offer)
}

// ArmOrigin records that this request should stay on origin.
func (h *AgentFulfillHub) ArmOrigin(requestID string) {
	if h == nil || requestID == "" {
		return
	}
	h.signal(requestID, &AgentFulfillOffer{Local: false})
}

func (h *AgentFulfillHub) signal(requestID string, offer *AgentFulfillOffer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.waiting[requestID]; ok {
		delete(h.waiting, requestID)
		select {
		case ch <- offer:
		default:
		}
		return
	}
	h.pending[requestID] = offer
	// Best-effort GC of stale pending.
	go h.expirePending(requestID, h.ttl)
}

func (h *AgentFulfillHub) expirePending(requestID string, d time.Duration) {
	time.Sleep(d)
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.pending, requestID)
}

// Wait waits up to timeout for a BidiAppend decision on requestID.
// Returns (nil, false) on timeout — caller should origin-passthrough.
func (h *AgentFulfillHub) Wait(requestID string, timeout time.Duration) (*AgentFulfillOffer, bool) {
	if h == nil || requestID == "" {
		return nil, false
	}
	if timeout <= 0 {
		timeout = defaultRunSSEFulfillWait
	}

	h.mu.Lock()
	if offer, ok := h.pending[requestID]; ok {
		delete(h.pending, requestID)
		h.mu.Unlock()
		return offer, true
	}
	ch := make(chan *AgentFulfillOffer, 1)
	h.waiting[requestID] = ch
	h.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case offer := <-ch:
		return offer, offer != nil
	case <-timer.C:
		h.mu.Lock()
		if cur, ok := h.waiting[requestID]; ok && cur == ch {
			delete(h.waiting, requestID)
		}
		// Race: BidiAppend may have stored pending just as we timed out.
		if offer, ok := h.pending[requestID]; ok {
			delete(h.pending, requestID)
			h.mu.Unlock()
			return offer, true
		}
		h.mu.Unlock()
		return nil, false
	}
}

// OpenTurnFamily records a cloud|local binding for rootRequestID and a short TTL
// window for summary/title follow-ons. Source may be explicit_* or decide_*.
// Replaces any prior family, except StickyLocal will not downgrade a live
// StickyCloud family (prevents mid-turn mis-routes from killing /cloud sticky).
func (h *AgentFulfillHub) OpenTurnFamily(rootRequestID string, mode StickyMode, source string) {
	if h == nil || mode == StickyNone {
		return
	}
	ttl := h.familyTTL
	if ttl <= 0 {
		ttl = DefaultTurnFamilyTTL
	}
	h.mu.Lock()
	if mode == StickyLocal && h.family != nil && h.family.Mode == StickyCloud {
		if h.family.ParentActive > 0 || time.Now().Before(h.family.Until) {
			// Keep cloud family; renew so wrap-up chrome still sticks.
			h.family.Until = time.Now().Add(ttl)
			h.mu.Unlock()
			return
		}
	}
	h.family = &turnFamily{
		Mode:          mode,
		RootRequestID: rootRequestID,
		Until:         time.Now().Add(ttl),
		Source:        source,
	}
	h.mu.Unlock()

	attrs := map[string]string{
		"route":           mode.String(),
		"source":          source,
		"root_request_id": rootRequestID,
	}
	g := h.graph()
	if g != nil && ttl > 0 {
		g.Grace = ttl
	}
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventTurnOpened,
		TurnID:    rootRequestID,
		RequestID: rootRequestID,
		Actor:     mode.String(),
		Attrs:     attrs,
	})
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventStickyBound,
		TurnID:    rootRequestID,
		RequestID: rootRequestID,
		Actor:     "mitm",
		Attrs:     attrs,
	})
}

// TouchTurnFamily renews the live family's TTL (call on sticky binds / parent run).
func (h *AgentFulfillHub) TouchTurnFamily() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.family == nil {
		return
	}
	ttl := h.familyTTL
	if ttl <= 0 {
		ttl = DefaultTurnFamilyTTL
	}
	h.family.Until = time.Now().Add(ttl)
}

// BeginParentRun marks a root RunSSE as in-flight so StickyCloud survives past
// wall-clock TTL until the parent stream ends (+ grace on EndParentRun).
func (h *AgentFulfillHub) BeginParentRun(requestID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.family == nil {
		h.mu.Unlock()
		return
	}
	ttl := h.familyTTL
	if ttl <= 0 {
		ttl = DefaultTurnFamilyTTL
	}
	h.family.ParentActive++
	h.family.Until = time.Now().Add(ttl)
	root := h.family.RootRequestID
	h.mu.Unlock()
	h.graph().Append(contextgraph.Event{
		Kind:      contextgraph.EventRunSSEOpen,
		TurnID:    root,
		RequestID: requestID,
		Actor:     "mitm",
		Attrs:     map[string]string{"role": "parent"},
	})
}

// EndParentRun drops an in-flight parent RunSSE and extends grace TTL so
// final-summary / title chrome after the parent stream still sticks.
func (h *AgentFulfillHub) EndParentRun(requestID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.family == nil {
		h.mu.Unlock()
		return
	}
	if h.family.ParentActive > 0 {
		h.family.ParentActive--
	}
	ttl := h.familyTTL
	if ttl <= 0 {
		ttl = DefaultTurnFamilyTTL
	}
	h.family.Until = time.Now().Add(ttl)
	root := h.family.RootRequestID
	h.mu.Unlock()
	h.graph().Append(contextgraph.Event{
		Kind:      contextgraph.EventRunSSEClose,
		TurnID:    root,
		RequestID: requestID,
		Actor:     "mitm",
		Attrs:     map[string]string{"role": "parent"},
	})
}

// SetFamilyTTL configures turn-family lifetime (from routing.turn_family_ttl).
// Zero or negative resets to DefaultTurnFamilyTTL.
func (h *AgentFulfillHub) SetFamilyTTL(d time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if d <= 0 {
		h.familyTTL = DefaultTurnFamilyTTL
	} else {
		h.familyTTL = d
	}
	ttl := h.familyTTL
	h.mu.Unlock()
	if g := h.graph(); g != nil {
		g.Grace = ttl
	}
}

// FamilyTTL returns the configured turn-family TTL.
func (h *AgentFulfillHub) FamilyTTL() time.Duration {
	if h == nil {
		return DefaultTurnFamilyTTL
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.familyTTL <= 0 {
		return DefaultTurnFamilyTTL
	}
	return h.familyTTL
}

// LookupTurnFamily returns the live turn family from the TTL map, if any.
// Live when Until is in the future OR a parent RunSSE is still in-flight.
// Graph fallback is intentionally NOT done here — short crumbs / unrelated
// local arms must not inherit a stale cloud turn from the process-wide store.
// Use ShouldStickyCloudOrigin / InheritTurnFollowOn for graph correlation.
func (h *AgentFulfillHub) LookupTurnFamily() (mode StickyMode, rootID, source string, ok bool) {
	if h == nil {
		return StickyNone, "", "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.family == nil {
		return StickyNone, "", "", false
	}
	if h.family.ParentActive <= 0 && time.Now().After(h.family.Until) {
		h.family = nil
		return StickyNone, "", "", false
	}
	return h.family.Mode, h.family.RootRequestID, h.family.Source, true
}

// InheritTurnFollowOn returns the turn-family mode only when userText looks like an
// immediate chrome follow-on (reply summary / title / final wrap-up). Real user
// messages → false so the classifier can offload local on the next turn after /cloud.
// requestID, when set, is bound into the turn graph for later RunSSE correlation.
func (h *AgentFulfillHub) InheritTurnFollowOn(userText string, requestID ...string) (StickyMode, string, bool) {
	if h == nil || !IsTurnFollowOn(userText) {
		return StickyNone, "", false
	}
	mode, root, src, ok := h.lookupCloudOrLocalFamily(true)
	if !ok || mode == StickyNone {
		return StickyNone, "", false
	}
	h.TouchTurnFamily()
	child := ""
	if len(requestID) > 0 {
		child = strings.TrimSpace(requestID[0])
	}
	attrs := map[string]string{"source": src, "preview": truncateForGraph(userText, 80)}
	h.graph().Append(contextgraph.Event{
		Kind:      contextgraph.EventSummaryRequested,
		TurnID:    root,
		RequestID: child,
		Actor:     mode.String(),
		Attrs:     attrs,
	})
	if child != "" && child != root {
		h.graph().BindRequest(root, child)
	}
	return mode, src, true
}

// ShouldStickyCloudOrigin reports whether this BidiAppend should stay on origin
// while a StickyCloud family is live (summary / child / tool-result / short chrome).
// Consults the TTL map first, then the context graph for strong follow-ons
// (summary/subagent) or already-bound request/session ids — never sticks a bare
// short crumb to an unrelated live cloud turn (avoids local→cloud false sticky).
func (h *AgentFulfillHub) ShouldStickyCloudOrigin(userText string, r *http.Request, body []byte) (root, source string, ok bool) {
	if h == nil {
		return "", "", false
	}
	strong := IsTurnFollowOnBody(userText, body) || IsSubagentOrChildTurn(userText, r, body)
	weak := !LooksLikeNewUserTurn(userText, body)
	if !strong && !weak {
		return "", "", false
	}

	mode, famRoot, src, famOK := h.LookupTurnFamily()
	if famOK && mode == StickyCloud {
		h.TouchTurnFamily()
		return famRoot, src, true
	}
	// Live StickyLocal family must not be overridden by a graph cloud turn.
	if famOK && mode == StickyLocal {
		return "", "", false
	}

	sess := ClientSessionKey(r)
	reqHint := ""
	if r != nil {
		reqHint = strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if reqHint == "" {
			reqHint = strings.TrimSpace(r.Header.Get("x-request-id"))
		}
	}
	g := h.graph()
	if g == nil {
		return "", "", false
	}

	// Already bound into a live cloud turn?
	if reqHint != "" {
		if tid := g.TurnIDForRequest(reqHint); tid != "" && g.CloudTurnLive(tid) {
			v, _ := g.Turn(tid)
			h.rehydrateCloudFamily(v.RootRequestID, v.Source)
			return v.RootRequestID, v.Source, true
		}
	}
	if sess != "" {
		if tid := g.TurnIDForSession(sess); tid != "" && g.CloudTurnLive(tid) {
			v, _ := g.Turn(tid)
			h.rehydrateCloudFamily(v.RootRequestID, v.Source)
			return v.RootRequestID, v.Source, true
		}
	}

	// Strong chrome/subagent only: inherit newest live cloud family (final summary
	// with new UUID after TTL wipe while parent RunSSE still open on the graph).
	if !strong {
		return "", "", false
	}
	tid, gSrc, gRoot, live := g.LiveCloudFamily()
	if !live {
		return "", "", false
	}
	h.rehydrateCloudFamily(gRoot, gSrc)
	_ = tid
	return gRoot, gSrc, true
}

// lookupCloudOrLocalFamily returns TTL family, or rehydrates StickyCloud from the
// graph when allowGraph is set (summary inherit path only).
func (h *AgentFulfillHub) lookupCloudOrLocalFamily(allowGraph bool) (mode StickyMode, rootID, source string, ok bool) {
	mode, rootID, source, ok = h.LookupTurnFamily()
	if ok {
		return mode, rootID, source, true
	}
	if !allowGraph {
		return StickyNone, "", "", false
	}
	tid, src, root, live := h.graph().LiveCloudFamily()
	if !live {
		return StickyNone, "", "", false
	}
	h.rehydrateCloudFamily(root, src)
	_ = tid
	return StickyCloud, root, src, true
}

func (h *AgentFulfillHub) rehydrateCloudFamily(root, source string) {
	if h == nil || root == "" {
		return
	}
	h.mu.Lock()
	ttl := h.familyTTL
	if ttl <= 0 {
		ttl = DefaultTurnFamilyTTL
	}
	opens := 0
	if g := h.graph(); g != nil {
		if v, ok := g.Turn(root); ok {
			opens = v.OpenRuns
			if source == "" {
				source = v.Source
			}
		}
	}
	h.family = &turnFamily{
		Mode:          StickyCloud,
		RootRequestID: root,
		Until:         time.Now().Add(ttl),
		Source:        source,
		ParentActive:  opens,
	}
	h.mu.Unlock()
}

func truncateForGraph(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// turnFollowOnRE matches Cursor chrome prompts that belong to the same user turn
// (reply summary, title generation, final wrap-up) — not a new user message.
var turnFollowOnRE = regexp.MustCompile(`(?i)\b(` +
	`summariz(?:e|ing|ation)|summary|` +
	`reply\s+summary|final\s+summary|brief\s+summary|executive\s+summary|` +
	`one[\s-]?sentence|one[\s-]?line\s+summary|short\s+(?:title|summary|description)|` +
	`generate\s+(?:a\s+)?title|title\s+(?:for|of|generation)|` +
	`conversation\s+title|thread\s+title|chat\s+title|` +
	`name\s+this\s+(?:chat|composer|conversation)|` +
	`completed_subtitle|final_summary|wrap[\s-]?up|` +
	`what\s+was\s+(?:done|changed)|concise\s+summary` +
	`)\b`)

// IsTurnFollowOn reports whether text looks like an immediate Agent chrome follow-on
// for the current turn (summary / title / wrap-up), not a new user instruction.
func IsTurnFollowOn(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	return turnFollowOnRE.MatchString(t)
}

// IsTurnFollowOnBody also peeks protobuf/printable body for chrome summary keywords
// when TipTap extract picked a short crumb (e.g. "Hi!") instead of the system prompt.
func IsTurnFollowOnBody(text string, body []byte) bool {
	if IsTurnFollowOn(text) {
		return true
	}
	if len(body) == 0 {
		return false
	}
	// Cap scan for hot path; keywords appear early in chrome packs.
	scan := body
	if len(scan) > 512<<10 {
		scan = scan[:512<<10]
	}
	return turnFollowOnRE.Match(scan)
}

// subagentPromptRE matches Cursor Task / subagent delegated prompts. These arrive
// as separate root-looking BidiAppend+RunSSE pairs (often without parent headers)
// and must not re-decide local while a StickyCloud turn family is live.
var subagentPromptRE = regexp.MustCompile(`(?i)\b(` +
	`subagent|` +
	`generalpurpose\s+subagent|` +
	`you are a (?:cursor )?subagent|` +
	`the user asked you to|` +
	`user asked (?:you |for )|` +
	`say hi via subagent|` +
	`through a subagent|` +
	`local fallback` +
	`)\b`)

// toolCallIDInBodyRE matches Cursor tool-call ids embedded in child context packs.
var toolCallIDInBodyRE = regexp.MustCompile(`call-[0-9a-f]{8}-[0-9a-f]{4}-`)

// maxChildBodyScan is how far we search for call-… markers (tool-result packs
// often inflate past 200 KiB; older 200 KiB cap caused StickyCloud leaks).
const maxChildBodyScan = 4 << 20

// IsSubagentOrChildTurn reports mid-turn child/subagent work that should inherit
// a live StickyCloud family (never local-fulfill during /cloud).
func IsSubagentOrChildTurn(userText string, r *http.Request, body []byte) bool {
	if cursorrpc.IsChildAgentRequest(r) {
		return true
	}
	if subagentPromptRE.MatchString(userText) {
		return true
	}
	// Delegated packs often embed call-… ids without parent HTTP headers (Task tool).
	if bodyHasToolCallID(body) {
		t := strings.TrimSpace(userText)
		// Empty extract or short chrome crumb still counts as mid-turn child work.
		if t == "" || len(t) < 400 {
			if !router.HasCloudOverride(t) && !router.HasLocalOverride(t) {
				return true
			}
		}
	}
	return false
}

func bodyHasToolCallID(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	scan := body
	if len(scan) > maxChildBodyScan {
		scan = scan[:maxChildBodyScan]
	}
	return toolCallIDInBodyRE.Match(scan)
}

// LooksLikeNewUserTurn reports a deliberate next-user instruction (re-decide),
// as opposed to chrome wrap-up / tool-result continuation during a cloud family.
func LooksLikeNewUserTurn(userText string, body []byte) bool {
	if IsTurnFollowOnBody(userText, body) || IsSubagentOrChildTurn(userText, nil, body) {
		return false
	}
	t := strings.TrimSpace(userText)
	if t == "" {
		return false
	}
	// Very short crumbs ("Hi!", "ok") during a live cloud family are almost always
	// mid-turn chrome / tool echo, not a new Composer send.
	if len(t) < 16 {
		return false
	}
	return true
}

// ResetFamilyForTest clears the in-memory TTL sticky map without touching the
// context graph. Used to prove summary/child sticky consults the graph.
func (h *AgentFulfillHub) ResetFamilyForTest() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.family = nil
}

// ClientSessionKey is an optional correlation id for metrics (x-session-id or CONNECT).
// It is NOT used for routing sticky — sticky is turn-family only.
func ClientSessionKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if sid := strings.TrimSpace(r.Header.Get("x-session-id")); sid != "" {
		return "xs:" + sid
	}
	if sid := strings.TrimSpace(r.Header.Get("X-Session-Id")); sid != "" {
		return "xs:" + sid
	}
	if cs := ConnectSessionFrom(r.Context()); cs != "" {
		return "cs:" + cs
	}
	return ""
}
