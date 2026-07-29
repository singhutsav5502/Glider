package mitm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
)

// Harness is the shared Tokenizer → Router → Transform → Execute pipeline.
// MITM calls CompleteLocal so non-local decisions become origin passthrough.
type Harness interface {
	CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

// Interceptor attempts local fulfillment for OpenAI-compatible (and Responses) bodies
// and fulfillable Cursor Agent Connect/protobuf chat RPCs through the same harness.
// Unrecognized or non-local decisions result in handled=false (origin passthrough).
type Interceptor struct {
	Harness     Harness
	Metrics     *metrics.Collector
	Log         *slog.Logger
	Passthrough bool // if true, never fulfill locally
	// Debug enables Path B R&D observability (logs + dumps + recent ring). Optional.
	Debug *AgentRPCDebugger
	// AgentRPCFulfill enables experimental Path B: BidiAppend extract → DecideLocal →
	// RunSSE text fulfill when codec + correlation succeed; else origin passthrough.
	AgentRPCFulfill bool
	// CannedOnError: when CompleteLocal fails after a local arm, write a canned
	// RunSSE text stream instead of origin fail-soft (Path B codec dry-run).
	CannedOnError bool
	// CannedText used when CannedOnError fires; empty → DefaultCannedRunSSEText.
	CannedText string
	// SurfaceLocalError: when CompleteLocal fails and canned is off, write a clear
	// Glider/Ollama error via RunSSE instead of Cursor origin fail-soft.
	// False (default) preserves hybrid origin fallback. Pure-local sets true.
	SurfaceLocalError bool
	// LocalHealthy optionally gates ArmLocal (RequireLocalHealthy). Nil → skip gate.
	LocalHealthy func() bool
	// FulfillHub correlates BidiAppend ↔ RunSSE by request UUID. Nil → package default.
	FulfillHub *AgentFulfillHub
	// ToolFollowup is routing.tool_followup (child RunSSE re-decide). Copied from config.
	ToolFollowup config.ToolFollowupConfig
	// AgentRPCToolCodec enables Path B child/tool RunSSE fulfill when PreferLocal.
	// Mirrors mitm.agent_rpc_tool_codec; also sets cursorrpc.SetRunSSEToolCodecEnabled.
	AgentRPCToolCodec bool
}

// DefaultCannedRunSSEText is the synthetic assistant reply used without a local backend.
const DefaultCannedRunSSEText = "pong from glider (canned Path B)"

// LocalDecider is optionally implemented by Harness (PipelineCompleter) for Phase 3.
type LocalDecider interface {
	DecideLocal(r *http.Request, req *backend.CompletionRequest) (*backend.RoutingDecision, error)
}

func (i *Interceptor) fulfillHub() *AgentFulfillHub {
	if i != nil && i.FulfillHub != nil {
		return i.FulfillHub
	}
	return defaultAgentFulfillHub
}

// allowArmLocal applies the Ollama health gate when LocalHealthy is set.
// Unhealthy + hybrid (!SurfaceLocalError) → skip arm (fail-soft origin on RunSSE wait).
// Unhealthy + pure-local (SurfaceLocalError) → still arm so RunSSE can surface a clear error.
func (i *Interceptor) allowArmLocal(log *slog.Logger, corrID string) bool {
	if i == nil || i.LocalHealthy == nil {
		return true
	}
	if i.LocalHealthy() {
		return true
	}
	if log != nil {
		log.Warn("mitm local health gate: backend unhealthy",
			"corr_id", corrID,
			"surface_local_error", i.SurfaceLocalError,
		)
	}
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "bidi_local_unhealthy")
	}
	return i.SurfaceLocalError
}

// TryHandle implements LocalHandler.
func (i *Interceptor) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	start := time.Now()
	log := i.log()
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}

	if i == nil || i.Passthrough || i.Harness == nil {
		return false, nil
	}
	if r.Method != http.MethodPost {
		return false, nil
	}

	kind := ClassifyPath(path)
	switch kind {
	case PathOpenAICompat:
		return i.handleOpenAI(w, r, host, path, start)
	case PathAgentRPC:
		return i.handleAgentRPC(w, r, host, path, start)
	default:
		if i.Debug != nil && i.Debug.Enabled {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
			outcome := "skip"
			if kind == PathCursorControl {
				outcome = "skip_control"
			}
			i.Debug.Observe(r, body, outcome)
		}
		i.logSkip(log, kind, host, path, r.Method, r.Header.Get("Content-Type"))
		return false, nil
	}
}

func (i *Interceptor) handleOpenAI(w http.ResponseWriter, r *http.Request, host, path string, start time.Time) (bool, error) {
	log := i.log()
	isResponses := strings.HasSuffix(path, "/responses") || path == "/v1/responses"

	body, err := io.ReadAll(r.Body)
	if err != nil {
		i.observe("error", host, path, "", "", "", 0, start)
		return false, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	body = api.NormalizeAnthropicShapedJSON(body)

	if i.Debug != nil && i.Debug.Enabled {
		i.Debug.Observe(r, body, "openai_compat")
	}

	var req *backend.CompletionRequest
	var responsesMode bool
	if isResponses {
		req, err = api.ResponsesToCompletion(body)
		if err != nil {
			log.Info("mitm skip unparsed responses body → origin", "host", host, "path", path, "err", err)
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "skip")
			}
			return false, nil
		}
		responsesMode = true
	} else if api.LooksLikeResponses(body) {
		req, err = api.ResponsesToCompletion(body)
		if err != nil {
			log.Info("mitm skip unparsed responses-shaped chat body → origin", "host", host, "path", path, "err", err)
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "skip")
			}
			return false, nil
		}
		responsesMode = true
	} else {
		req, err = api.ParseCompletionRequest(body)
		if err != nil {
			log.Info("mitm skip unparsed chat body → origin", "host", host, "path", path, "err", err)
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "skip")
			}
			return false, nil
		}
	}
	req.Model = api.NormalizeGatewayModel(req.Model)

	if req.Metadata.RequestID == "" {
		req.Metadata.RequestID = fmt.Sprintf("mitm_%d", time.Now().UnixNano())
	}
	req.Metadata.OriginalModel = req.Model
	if req.Metadata.Priority == 0 {
		req.Metadata.Priority = backend.PriorityHigh
	}

	log.Info("mitm intercept",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"model", req.Model,
		"stream", req.Stream,
		"responses", responsesMode,
		"bytes", len(body),
	)

	chunks, err := i.Harness.CompleteLocal(r, req)
	if errors.Is(err, orchestrator.ErrOriginPassthrough) {
		log.Info("mitm origin passthrough",
			"id", req.Metadata.RequestID,
			"host", host,
			"path", path,
			"model", req.Model,
			"tokens", req.Metadata.EstimatedTokens,
			"latency_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
		return false, nil
	}
	if err != nil {
		log.Error("mitm local fulfill failed", "id", req.Metadata.RequestID, "host", host, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return true, nil
	}

	log.Info("mitm local fulfill",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"model", req.Model,
		"tokens", req.Metadata.EstimatedTokens,
	)

	if responsesMode {
		if req.Stream {
			return true, api.WriteResponsesSSE(w, req.Metadata.RequestID, req.Model, chunks)
		}
		return true, api.WriteResponsesJSON(w, req.Metadata.RequestID, req.Model, chunks)
	}
	if req.Stream {
		return true, writeChatSSE(w, req.Metadata.RequestID, req.Model, chunks)
	}
	return true, writeChatJSON(w, req.Metadata.RequestID, req.Model, chunks)
}

func (i *Interceptor) handleAgentRPC(w http.ResponseWriter, r *http.Request, host, path string, start time.Time) (bool, error) {
	log := i.log()
	ct := r.Header.Get("Content-Type")

	// agent.v1.AgentService/Run specifically (not RunSSE, BidiAppend, or
	// StreamChat*) is a genuine bidi-streaming RPC whose real client keeps
	// its request stream's send side open for ~30s, sending periodic
	// keepalive envelopes — a blocking io.ReadAll(r.Body) here silently
	// eats that whole window before this handler (or origin passthrough
	// below) can respond at all, long enough for the client to give up
	// and reset the stream first. Real, live-confirmed incident (see
	// cursorrpc.ReadFirstEnvelope's own doc comment) — DelegateHandler,
	// which runs before this handler in the chain, was fixed the same
	// way (2026-07-29); this fixes the same latent issue for the plain,
	// non-delegate passthrough path this handler covers. The human's
	// prompt is always the first envelope on this call shape, so reading
	// only it is not lossy — other AgentRPC shapes keep the plain
	// io.ReadAll since they don't share this bidi-streaming concern.
	var body []byte
	var err error
	if cursorrpc.IsAgentServiceRunPath(path) {
		body, err = cursorrpc.ReadFirstEnvelope(r.Body)
	} else {
		body, err = io.ReadAll(r.Body)
	}
	if err != nil {
		i.observe("error", host, path, "", "", "", 0, start)
		return false, err
	}
	_ = r.Body.Close()
	// Always restore original bytes for origin passthrough.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	decoded, err := cursorrpc.DecodeChatRequest(path, body)
	if err != nil {
		log.Info("mitm agent rpc decode failed → origin",
			"host", host, "path", path, "content_type", ct, "bytes", len(body), "err", err)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "agent_rpc_decode_err")
		}
		if i.Debug != nil && i.Debug.Enabled {
			i.Debug.Observe(r, body, "agent_rpc_decode_err")
		}
		return false, nil
	}
	if decoded == nil || decoded.Request == nil {
		// Opaque modern Agent path (BidiAppend / RunSSE / ChatService…).
		i.maybeBidiExtractDecide(r, body, host, path, ct, log)
		if handled, herr := i.tryRunSSEFulfill(w, r, body, host, path, start, log); handled || herr != nil {
			return handled, herr
		}
		log.Info("mitm agent rpc opaque (no schema decode) → origin",
			"host", host, "path", path, "content_type", ct, "bytes", len(body),
			"fulfillable_path", cursorrpc.IsFulfillableAgentPath(path),
		)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "agent_rpc_opaque")
		}
		if i.Debug != nil && i.Debug.Enabled {
			i.Debug.Observe(r, body, "agent_rpc_opaque")
		}
		return false, nil
	}
	if len(decoded.Request.Messages) == 0 {
		log.Info("mitm agent rpc empty conversation → origin",
			"host", host, "path", path, "kind", decoded.Kind.String())
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "agent_rpc_opaque")
		}
		if i.Debug != nil && i.Debug.Enabled {
			i.Debug.Observe(r, body, "agent_rpc_empty")
		}
		return false, nil
	}

	req := decoded.Request
	req.Model = api.NormalizeGatewayModel(req.Model)
	if req.Metadata.RequestID == "" {
		req.Metadata.RequestID = fmt.Sprintf("mitm_agent_%d", time.Now().UnixNano())
	}
	req.Metadata.OriginalModel = req.Model
	if req.Metadata.Priority == 0 {
		req.Metadata.Priority = backend.PriorityHigh
	}
	req.Metadata.Adapter = "cursor_agent_rpc"

	log.Info("mitm agent rpc intercept",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"kind", decoded.Kind.String(),
		"model", req.Model,
		"messages", len(req.Messages),
		"bytes", len(body),
	)
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "agent_rpc_decode")
	}
	if i.Debug != nil && i.Debug.Enabled {
		i.Debug.Observe(r, body, "agent_rpc_decode")
	}

	chunks, err := i.Harness.CompleteLocal(r, req)
	if errors.Is(err, orchestrator.ErrOriginPassthrough) {
		log.Info("mitm agent rpc origin passthrough",
			"id", req.Metadata.RequestID,
			"host", host,
			"path", path,
			"model", req.Model,
			"tokens", req.Metadata.EstimatedTokens,
			"latency_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "agent_rpc_passthrough")
		}
		// Original body already restored on r.
		return false, nil
	}
	if err != nil {
		log.Error("mitm agent rpc local fulfill failed", "id", req.Metadata.RequestID, "host", host, "err", err)
		_ = cursorrpc.FailClosedConnect(w, err.Error())
		return true, nil
	}

	if !decoded.Fulfillable() {
		msg := cursorrpc.LocalFulfillError(path).Error()
		log.Error("mitm agent rpc fail-closed (no response codec)",
			"id", req.Metadata.RequestID, "path", path)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "agent_rpc_fail_closed")
		}
		_ = cursorrpc.FailClosedConnect(w, msg)
		return true, nil
	}

	log.Info("mitm agent rpc local fulfill",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"kind", decoded.Kind.String(),
		"model", req.Model,
		"tokens", req.Metadata.EstimatedTokens,
	)
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "agent_rpc_local")
	}
	return true, cursorrpc.WriteStreamChatResponse(w, chunks)
}

// maybeBidiExtractDecide runs Phase 1–3 experimental path for BidiAppend:
// extract context_envelope → DecideLocal → would_fulfill_local (still passthrough).
// Enabled when AgentRPCFulfill is set, or when debug is on (extract logged only).
func (i *Interceptor) maybeBidiExtractDecide(r *http.Request, body []byte, host, path, ct string, log *slog.Logger) {
	if i == nil {
		return
	}
	debugOn := i.Debug != nil && i.Debug.Enabled
	if !i.AgentRPCFulfill && !debugOn {
		return
	}
	if !cursorrpc.IsBidiAppendPath(path) {
		return
	}
	ce := ""
	if r != nil {
		ce = r.Header.Get("Content-Encoding")
	}
	wire, _, _ := debugBodyForWire(body, ce)
	if len(wire) == 0 {
		wire = body
	}
	ext, err := cursorrpc.ExtractBidiCompletionRequest(wire)
	if err != nil || ext == nil || ext.Request == nil {
		return
	}
	req := ext.Request
	req.Model = api.NormalizeGatewayModel(req.Model)
	if req.Metadata.RequestID == "" {
		req.Metadata.RequestID = fmt.Sprintf("mitm_bidi_%d", time.Now().UnixNano())
	}

	log.Info("mitm bidi extract",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"model", req.Model,
		"user_chars", len(ext.UserText),
		"source", ext.Source,
	)
	if ext.Inspect != nil && ext.Inspect.RequestID != "" {
		log.Info("mitm bidi extract correlation",
			"id", req.Metadata.RequestID,
			"bidi_request_id", ext.Inspect.RequestID,
		)
	}
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "bidi_extract")
	}
	if debugOn {
		// Attach extract summary onto the next Observe via temporary fields is hard;
		// log is enough; Observe still runs with bidi_inspect.
		_ = ct
	}
	if !i.AgentRPCFulfill {
		return
	}

	corrID := req.Metadata.RequestID
	if ext.Inspect != nil && ext.Inspect.RequestID != "" {
		corrID = ext.Inspect.RequestID
	}
	hub := i.fulfillHub()
	sess := ClientSessionKey(r)
	i.emitContextEvent(contextgraph.EventBidiSeen, corrID, "", "mitm", map[string]string{
		"source":  ext.Source,
		"preview": truncateRunes(ext.UserText, 80),
	}, sess)

	// Priority: explicit /cloud|/heavy → explicit /local|/fast → turn-family
	// follow-on sticky (summary/title only) → classifier. Never conversation-wide.
	// Hard-force: /cloud|/heavy in TipTap/user text never arms local fulfill.
	if router.HasCloudOverride(ext.UserText) || requestHasCloudOverride(req) {
		log.Info("mitm bidi extract /cloud hard-force → origin",
			"id", req.Metadata.RequestID,
			"corr_id", corrID,
			"user_preview", truncateRunes(ext.UserText, 64),
		)
		hub.ArmOrigin(corrID)
		hub.OpenTurnFamily(corrID, StickyCloud, "explicit_cloud")
		if sess != "" {
			hub.graph().BindSession(corrID, sess)
		}
		i.recordPathB(r, "origin_passthrough", "cloud", host, path, req.Model, req.Metadata.OriginalModel,
			"explicit_cloud", req.Metadata.EstimatedTokens, corrID, time.Now())
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "bidi_decide_passthrough")
			i.Metrics.IncAction("mitm", "bidi_cloud_override")
		}
		i.emitContextEvent(contextgraph.EventRouteDecided, corrID, corrID, "cloud", map[string]string{
			"source": "explicit_cloud", "route": "cloud",
		}, sess)
		return
	}

	decider, ok := i.Harness.(LocalDecider)
	if !ok || decider == nil {
		log.Debug("mitm bidi extract: harness has no DecideLocal")
		return
	}

	// Explicit /local|/fast hard-force (beats turn-family sticky and classifiers).
	if router.HasLocalOverride(ext.UserText) || requestHasLocalOverride(req) {
		decision, err := decider.DecideLocal(r, req)
		if err != nil {
			log.Info("mitm bidi /local DecideLocal failed → origin", "id", req.Metadata.RequestID, "err", err)
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "bidi_decide_err")
			}
			return
		}
		if decision == nil || !orchestrator.IsLocalTarget(decision.Target) {
			decision = &backend.RoutingDecision{
				Target:   "local",
				RuleName: "explicit_local",
				Reason:   "explicit_override",
				Model:    req.Model,
			}
		}
		if !i.allowArmLocal(log, corrID) {
			log.Info("mitm bidi /local skipped — local unhealthy", "corr_id", corrID)
			return
		}
		hub.ArmLocal(corrID, &AgentFulfillOffer{
			Local:    true,
			Request:  req,
			Decision: decision,
			UserText: ext.UserText,
			Source:   ext.Source,
		})
		hub.OpenTurnFamily(corrID, StickyLocal, "explicit_local")
		if sess != "" {
			hub.graph().BindSession(corrID, sess)
		}
		log.Info("mitm bidi /local hard-force → fulfill armed",
			"id", req.Metadata.RequestID,
			"corr_id", corrID,
			"rule", decision.RuleName,
			"user_preview", truncateRunes(ext.UserText, 64),
		)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "bidi_fulfill_armed")
			i.Metrics.IncAction("mitm", "bidi_local_override")
			i.Metrics.IncAction("mitm", "would_fulfill_local")
		}
		i.emitContextEvent(contextgraph.EventRouteDecided, corrID, corrID, "local", map[string]string{
			"source": "explicit_local", "route": "local", "rule": decision.RuleName,
		}, sess)
		return
	}

	// StickyCloud belt: mid-turn children, final-summary/title chrome, and short
	// tool-result crumbs must stay origin for the full parent cloud turn.
	if root, src, stick := hub.ShouldStickyCloudOrigin(ext.UserText, ext.Source, r, wire); stick {
		rule := "turn_family_cloud"
		metric := "bidi_sticky_cloud"
		scan := chromeScanBytes(ext.UserText, wire)
		if IsSubagentOrChildTurn(ext.UserText, r, wire) {
			rule = "turn_family_cloud_child"
			metric = "bidi_sticky_cloud_child"
			i.emitContextEvent(contextgraph.EventSubagentSpawned, corrID, root, "cloud", map[string]string{
				"source": src, "preview": truncateRunes(ext.UserText, 80),
			}, sess)
		} else if IsComposerWrapUpChrome(ext.UserText, ext.Source, scan) ||
			IsSystemSummaryChrome(ext.UserText, scan) ||
			!IsAllowlistedFreshTipTapUser(ext.UserText, ext.Source, scan) {
			rule = "turn_family_cloud_summary"
			metric = "bidi_sticky_cloud_summary"
			i.emitContextEvent(contextgraph.EventSummaryRequested, corrID, root, "cloud", map[string]string{
				"source": src, "preview": truncateRunes(ext.UserText, 80),
			}, sess)
		}
		log.Info("mitm bidi sticky-cloud → origin",
			"id", req.Metadata.RequestID,
			"corr_id", corrID,
			"family_root", root,
			"family_source", src,
			"rule", rule,
			"metric", metric,
			"user_preview", truncateRunes(ext.UserText, 64),
		)
		hub.ArmOrigin(corrID)
		if root != "" && corrID != "" && root != corrID {
			hub.graph().BindRequest(root, corrID)
		}
		if sess != "" && root != "" {
			hub.graph().BindSession(root, sess)
		}
		i.recordPathB(r, "origin_passthrough", "cloud", host, path, req.Model, req.Metadata.OriginalModel,
			rule, req.Metadata.EstimatedTokens, corrID, time.Now())
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "bidi_decide_passthrough")
			i.Metrics.IncAction("mitm", metric)
			// Keep legacy bidi_sticky_cloud counter for dashboards when summary-specific.
			if metric == "bidi_sticky_cloud_summary" {
				i.Metrics.IncAction("mitm", "bidi_sticky_cloud")
			}
		}
		i.emitContextEvent(contextgraph.EventOriginPassthrough, corrID, root, "cloud", map[string]string{
			"source": src, "rule": rule,
		}, sess)
		return
	}

	// composer_wrapup_origin belt: chrome wrap-up / title packs always ArmOrigin
	// even when StickyCloud TTL+grace already expired (dump leak: "Friday stock
	// market close" + user_visible_high_level_summary → Small Context Local).
	scanWrap := chromeScanBytes(ext.UserText, wire)
	i.annotateComposerWrapupMeta(req, hub, ext, scanWrap, sess, corrID)
	if i.tryArmComposerWrapupOrigin(r, req, hub, ext, scanWrap, corrID, sess, host, path) {
		return
	}

	// Turn-family sticky local: reply-summary / title follow-ons inherit local.
	if mode, src, ok := hub.InheritTurnFollowOn(ext.UserText, corrID); ok {
		if mode == StickyCloud {
			// Covered by ShouldStickyCloudOrigin; keep as belt.
			log.Info("mitm bidi turn-family cloud follow-on → origin",
				"id", req.Metadata.RequestID,
				"corr_id", corrID,
				"family_source", src,
				"user_preview", truncateRunes(ext.UserText, 64),
			)
			hub.ArmOrigin(corrID)
			i.recordPathB(r, "origin_passthrough", "cloud", host, path, req.Model, req.Metadata.OriginalModel,
				"turn_family_cloud", req.Metadata.EstimatedTokens, corrID, time.Now())
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "bidi_decide_passthrough")
				i.Metrics.IncAction("mitm", "bidi_sticky_cloud")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, corrID, "", "cloud", map[string]string{
				"source": src, "rule": "turn_family_cloud",
			}, sess)
			return
		}
		if mode == StickyLocal {
			i.annotateComposerWrapupMeta(req, hub, ext, chromeScanBytes(ext.UserText, wire), sess, corrID)
			decision, err := decider.DecideLocal(r, req)
			if err != nil {
				log.Info("mitm bidi turn-family local DecideLocal failed → origin", "id", req.Metadata.RequestID, "err", err)
				if i.Metrics != nil {
					i.Metrics.IncAction("mitm", "bidi_decide_err")
				}
				i.emitContextEvent(contextgraph.EventError, corrID, "", "mitm", map[string]string{
					"where": "turn_family_local", "err": err.Error(),
				}, sess)
				return
			}
			if decision == nil || !orchestrator.IsLocalTarget(decision.Target) {
				decision = &backend.RoutingDecision{
					Target:   "local",
					RuleName: "turn_family_local",
					Reason:   "turn_family_sticky",
					Model:    req.Model,
				}
			}
			if !i.allowArmLocal(log, corrID) {
				log.Info("mitm bidi turn-family local skipped — unhealthy", "corr_id", corrID)
				return
			}
			hub.ArmLocal(corrID, &AgentFulfillOffer{
				Local:    true,
				Request:  req,
				Decision: decision,
				UserText: ext.UserText,
				Source:   "turn_family_local",
			})
			log.Info("mitm bidi turn-family local follow-on → fulfill armed",
				"id", req.Metadata.RequestID,
				"corr_id", corrID,
				"family_source", src,
				"rule", decision.RuleName,
				"user_preview", truncateRunes(ext.UserText, 64),
			)
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "bidi_fulfill_armed")
				i.Metrics.IncAction("mitm", "bidi_sticky_local")
				i.Metrics.IncAction("mitm", "would_fulfill_local")
			}
			i.emitContextEvent(contextgraph.EventRouteDecided, corrID, "", "local", map[string]string{
				"source": "turn_family_local", "route": "local", "rule": decision.RuleName,
			}, sess)
			return
		}
	}

	i.annotateComposerWrapupMeta(req, hub, ext, chromeScanBytes(ext.UserText, wire), sess, corrID)
	decision, err := decider.DecideLocal(r, req)
	if err != nil {
		log.Info("mitm bidi DecideLocal failed → origin", "id", req.Metadata.RequestID, "err", err)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "bidi_decide_err")
		}
		i.emitContextEvent(contextgraph.EventError, corrID, "", "mitm", map[string]string{
			"where": "decide_local", "err": err.Error(),
		}, sess)
		return
	}
	if decision == nil {
		return
	}
	// Belt: never arm local for wrap-up / sticky-cloud non-fresh even if a lower
	// priority rule (Small Context Local) won — composer_wrapup_origin must win.
	if orchestrator.IsLocalTarget(decision.Target) {
		scan := chromeScanBytes(ext.UserText, wire)
		if i.tryArmComposerWrapupOrigin(r, req, hub, ext, scan, corrID, sess, host, path) {
			return
		}
	}
	if !orchestrator.IsLocalTarget(decision.Target) {
		log.Info("mitm bidi extract origin intent",
			"id", req.Metadata.RequestID,
			"target", decision.Target,
			"rule", decision.RuleName,
			"tokens", req.Metadata.EstimatedTokens,
		)
		hub.ArmOrigin(corrID)
		// Bind turn-family from DecideLocal cloud so reply-summary / same-turn
		// chrome follow-ons stay origin (not conversation-wide). Skip opening a
		// NEW family for composer_wrapup_origin alone — that would sticky-lock
		// the next real user turn after an expired /cloud grace window.
		if !strings.EqualFold(decision.RuleName, router.ComposerWrapupRuleName) {
			hub.OpenTurnFamily(corrID, StickyCloud, "decide_cloud")
			if sess != "" {
				hub.graph().BindSession(corrID, sess)
			}
		} else {
			hub.TouchTurnFamily()
			if sess != "" {
				if tid := hub.graph().TurnIDForSession(sess); tid != "" {
					hub.graph().BindRequest(tid, corrID)
				}
			}
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "bidi_composer_wrapup")
			}
		}
		i.recordPathB(r, "origin_passthrough", "cloud", host, path, req.Model, req.Metadata.OriginalModel,
			decision.RuleName, req.Metadata.EstimatedTokens, corrID, time.Now())
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "bidi_decide_passthrough")
			if !strings.EqualFold(decision.RuleName, router.ComposerWrapupRuleName) {
				i.Metrics.IncAction("mitm", "bidi_decide_cloud_family")
			}
		}
		i.emitContextEvent(contextgraph.EventRouteDecided, corrID, corrID, "cloud", map[string]string{
			"source": "decide_cloud", "route": "cloud", "rule": decision.RuleName,
		}, sess)
		i.emitContextEvent(contextgraph.EventOriginPassthrough, corrID, corrID, "cloud", map[string]string{
			"rule": decision.RuleName,
		}, sess)
		return
	}

	if !i.allowArmLocal(log, corrID) {
		log.Info("mitm bidi local decision skipped — local unhealthy → origin",
			"corr_id", corrID, "rule", decision.RuleName)
		hub.ArmOrigin(corrID)
		i.emitContextEvent(contextgraph.EventOriginPassthrough, corrID, corrID, "mitm", map[string]string{
			"rule": "local_unhealthy",
		}, sess)
		return
	}

	hub.ArmLocal(corrID, &AgentFulfillOffer{
		Local:    true,
		Request:  req,
		Decision: decision,
		UserText: ext.UserText,
		Source:   ext.Source,
	})
	hub.OpenTurnFamily(corrID, StickyLocal, "decide_local")
	if sess != "" {
		hub.graph().BindSession(corrID, sess)
	}
	log.Info("mitm bidi fulfill armed",
		"id", req.Metadata.RequestID,
		"corr_id", corrID,
		"host", host,
		"path", path,
		"model", req.Model,
		"rule", decision.RuleName,
		"tokens", req.Metadata.EstimatedTokens,
		"user_preview", truncateRunes(ext.UserText, 64),
		"codec", cursorrpc.HasRunSSETextCodec(),
	)
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "bidi_fulfill_armed")
		i.Metrics.IncAction("mitm", "bidi_decide_local_family")
		// Retain legacy metric name for dashboards during transition.
		i.Metrics.IncAction("mitm", "would_fulfill_local")
	}
	i.emitContextEvent(contextgraph.EventRouteDecided, corrID, corrID, "local", map[string]string{
		"source": "decide_local", "route": "local", "rule": decision.RuleName,
	}, sess)
}

// tryRunSSEFulfill waits for a correlated BidiAppend decision and, when local,
// runs CompleteLocal and writes a minimal AgentServerMessage stream (no origin).
func (i *Interceptor) tryRunSSEFulfill(w http.ResponseWriter, r *http.Request, body []byte, host, path string, start time.Time, log *slog.Logger) (bool, error) {
	if i == nil || !i.AgentRPCFulfill || !cursorrpc.HasRunSSETextCodec() {
		return false, nil
	}
	if !cursorrpc.IsRunSSEPath(path) {
		return false, nil
	}
	// Tool-loop child runs: re-decide via routing.tool_followup (not stuck on parent
	// cloud forever). Fulfill local only when Path B tool codec is ready; else
	// log would_local / origin and passthrough.
	if !cursorrpc.IsRootAgentRunSSE(r) {
		return i.evaluateToolFollowupRunSSE(w, r, body, host, path, start, log)
	}

	reqID := cursorrpc.ExtractRunSSERequestID(body)
	if reqID == "" {
		reqID = cursorrpc.RequestIDFromHeaders(r)
	}
	if reqID == "" {
		return false, nil
	}

	log.Info("mitm runsse waiting for bidi decision",
		"corr_id", reqID, "host", host, "path", path, "wait_ms", defaultRunSSEFulfillWait.Milliseconds())
	hub := i.fulfillHub()
	offer, ok := hub.Wait(reqID, defaultRunSSEFulfillWait)

	// Keep StickyCloud alive for the full parent cloud RunSSE (+ grace after end).
	if mode, _, _, famOK := hub.LookupTurnFamily(); famOK && mode == StickyCloud {
		if !ok || offer == nil || !offer.Local {
			hub.BeginParentRun(reqID)
			defer hub.EndParentRun(reqID)
		}
	}

	// Turn-family belt: refuse local/canned for summary/title/child while cloud family live.
	if ok && offer != nil && offer.Local {
		scanBody := body
		if offer.Request != nil && offer.Request.Metadata.WrapupScan != "" {
			scanBody = append(scanBody, []byte(offer.Request.Metadata.WrapupScan)...)
		}
		fresh := HasFreshUserTipTapTurn(offer.UserText, offer.Source, chromeScanBytes(offer.UserText, scanBody))
		wrapup := router.MatchComposerWrapup(offer.UserText, chromeScanBytes(offer.UserText, scanBody)) ||
			(offer.Request != nil && router.MatchComposerWrapup(offer.UserText, []byte(offer.Request.Metadata.WrapupScan)))

		if mode, _, _, famOK := hub.LookupTurnFamily(); famOK && mode == StickyCloud && !fresh {
			log.Info("mitm runsse StickyCloud refuses non-TipTap-new-user local → origin",
				"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_cloud_override")
				i.Metrics.IncAction("mitm", "runsse_sticky_cloud")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "cloud", map[string]string{
				"rule": "runsse_sticky_cloud_live",
			})
			return false, nil
		}
		if hub.graph().CloudTurnLive(reqID) && !fresh {
			log.Info("mitm runsse contextgraph cloud refuses non-fresh local → origin",
				"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_cloud_override")
				i.Metrics.IncAction("mitm", "runsse_sticky_cloud")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "cloud", map[string]string{
				"rule": "runsse_graph_cloud_live",
			})
			return false, nil
		}
		if wrapup || (offer.Decision != nil && strings.EqualFold(offer.Decision.RuleName, router.ComposerWrapupRuleName)) {
			log.Info("mitm runsse composer_wrapup_origin refuses local → origin",
				"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_cloud_override")
				i.Metrics.IncAction("mitm", "runsse_composer_wrapup")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "cloud", map[string]string{
				"rule": router.ComposerWrapupRuleName,
			})
			return false, nil
		}
		if _, _, stick := hub.ShouldStickyCloudOrigin(offer.UserText, offer.Source, r, body); stick {
			log.Info("mitm runsse turn-family cloud refuses local child/follow-on → origin",
				"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_cloud_override")
				i.Metrics.IncAction("mitm", "runsse_sticky_cloud")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "cloud", map[string]string{
				"rule": "runsse_sticky_cloud",
			})
			return false, nil
		}
		// Graph Bind/Lookup belt: request already bound into a live cloud turn.
		if hub.graph().CloudTurnLive(reqID) &&
			(IsTurnFollowOnBody(offer.UserText, body) || IsSystemSummaryChrome(offer.UserText, body) ||
				IsSubagentOrChildTurn(offer.UserText, r, body) || !LooksLikeNewUserTurn(offer.UserText, body)) {
			log.Info("mitm runsse contextgraph cloud refuses local follow-on → origin",
				"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_cloud_override")
				i.Metrics.IncAction("mitm", "runsse_sticky_cloud")
			}
			i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "cloud", map[string]string{
				"rule": "runsse_graph_sticky_cloud",
			})
			return false, nil
		}
	}

	if !ok || offer == nil || !offer.Local || offer.Request == nil {
		log.Info("mitm runsse no local offer → origin",
			"corr_id", reqID, "got_offer", ok,
			"local", offer != nil && offer.Local,
		)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_wait_origin")
		}
		i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, "", "mitm", map[string]string{
			"rule": "runsse_wait_origin",
		})
		return false, nil
	}

	// Belt: never local/canned fulfill when decision or user text says cloud.
	if offer.Decision != nil && !orchestrator.IsLocalTarget(offer.Decision.Target) {
		log.Info("mitm runsse cloud decision → origin (no canned)",
			"corr_id", reqID, "target", offer.Decision.Target, "rule", offer.Decision.RuleName)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_cloud_override")
		}
		return false, nil
	}
	if router.HasCloudOverride(offer.UserText) || requestHasCloudOverride(offer.Request) {
		log.Info("mitm runsse /cloud in user text → origin (no canned)",
			"corr_id", reqID, "user_preview", truncateRunes(offer.UserText, 64))
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_cloud_override")
		}
		return false, nil
	}

	req := offer.Request
	req.Model = api.NormalizeGatewayModel(req.Model)
	if req.Metadata.RequestID == "" {
		req.Metadata.RequestID = reqID
	}
	req.Metadata.Adapter = "cursor_runsse_fulfill"

	chunks, err := i.Harness.CompleteLocal(r, req)
	canned := false
	localErrSurface := false
	if errors.Is(err, orchestrator.ErrOriginPassthrough) {
		log.Info("mitm runsse CompleteLocal passthrough → origin",
			"corr_id", reqID, "model", req.Model)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_complete_passthrough")
		}
		return false, nil
	}
	if err != nil {
		// Always surface the real CompleteLocal failure before any canned path.
		log.Error("mitm runsse CompleteLocal failed",
			"corr_id", reqID,
			"err", err,
			"model", req.Model,
			"canned_on_error", i.CannedOnError,
		)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_complete_err")
		}
		if !i.CannedOnError {
			if i.SurfaceLocalError {
				msg := fmt.Sprintf("Glider local fulfill failed (Ollama/local backend): %v\n\nFix: start Ollama (`ollama serve`), pull the model, or set mitm.origin_on_local_error: true to fall back to Cursor origin.", err)
				log.Error("mitm runsse CompleteLocal failed → clear local error (no origin)",
					"corr_id", reqID, "err", err)
				if i.Metrics != nil {
					i.Metrics.IncAction("mitm", "runsse_local_error")
				}
				i.emitContextEvent(contextgraph.EventError, reqID, "", "local", map[string]string{
					"where": "complete_local", "err": err.Error(),
				})
				chunks = cursorrpc.CannedCompletionChunks(msg)
				localErrSurface = true
				rule := "local_error"
				if offer.Decision != nil && offer.Decision.RuleName != "" {
					rule = offer.Decision.RuleName + "+local_error"
				}
				i.recordPathB(r, "error", "local", host, path, req.Model, req.Metadata.OriginalModel,
					rule, req.Metadata.EstimatedTokens, reqID, start)
			} else {
				log.Info("mitm runsse CompleteLocal failed → origin fallback",
					"corr_id", reqID)
				return false, nil
			}
		} else {
			// Final guard: never canned when /cloud is present (even if arm was wrong).
			if router.HasCloudOverride(offer.UserText) || requestHasCloudOverride(req) {
				log.Info("mitm runsse skip canned — /cloud override",
					"corr_id", reqID)
				if i.Metrics != nil {
					i.Metrics.IncAction("mitm", "runsse_cloud_override")
				}
				return false, nil
			}
			text := strings.TrimSpace(i.CannedText)
			if text == "" {
				text = DefaultCannedRunSSEText
			}
			log.Error("mitm runsse CompleteLocal failed → canned fulfill (opt-in)",
				"corr_id", reqID, "err", err, "model", req.Model, "canned_chars", len(text))
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "runsse_canned")
			}
			chunks = cursorrpc.CannedCompletionChunks(text)
			canned = true
			rule := "canned"
			if offer.Decision != nil && offer.Decision.RuleName != "" {
				rule = offer.Decision.RuleName + "+canned"
			}
			i.recordPathB(r, "canned", "local", host, path, req.Model, req.Metadata.OriginalModel,
				rule, req.Metadata.EstimatedTokens, reqID, start)
		}
	} else {
		// Successful Ollama fulfill — always publish Overview local % (not only IncAction).
		rule := "local"
		if offer.Decision != nil && offer.Decision.RuleName != "" {
			rule = offer.Decision.RuleName
		}
		i.recordPathB(r, "local", "local", host, path, req.Model, req.Metadata.OriginalModel,
			rule, req.Metadata.EstimatedTokens, reqID, start)
	}

	log.Info("mitm runsse local fulfill",
		"corr_id", reqID,
		"host", host,
		"path", path,
		"model", req.Model,
		"tokens", req.Metadata.EstimatedTokens,
		"user_preview", truncateRunes(offer.UserText, 64),
		"canned", canned,
		"local_error", localErrSurface,
		"latency_ms", float64(time.Since(start).Microseconds())/1000.0,
	)
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "runsse_local")
		i.Metrics.IncAction("mitm", "agent_rpc_local")
	}
	if !localErrSurface {
		i.emitContextEvent(contextgraph.EventFulfilledLocal, reqID, "", "local", map[string]string{
			"canned": fmt.Sprintf("%v", canned),
		})
	}
	if i.Debug != nil && i.Debug.Enabled {
		kind := "agent_rpc_runsse_local"
		if canned {
			kind = "agent_rpc_runsse_canned"
		}
		if localErrSurface {
			kind = "agent_rpc_runsse_local_error"
		}
		i.Debug.Observe(r, body, kind)
	}
	if err := cursorrpc.WriteRunSSEResponse(w, chunks); err != nil {
		log.Error("mitm runsse encode/write failed", "corr_id", reqID, "err", err)
		if i.Metrics != nil {
			i.Metrics.IncAction("mitm", "runsse_encode_err")
		}
		i.emitContextEvent(contextgraph.EventError, reqID, "", "mitm", map[string]string{
			"where": "runsse_encode", "err": err.Error(),
		})
		// Response may be partially written — do not also dial origin.
		return true, err
	}
	return true, nil
}

// ApplyRoutingPolicy applies routing.turn_family_ttl and tool_followup onto this interceptor.
func (i *Interceptor) ApplyRoutingPolicy(routing config.RoutingConfig) {
	if i == nil {
		return
	}
	i.ToolFollowup = routing.ToolFollowup
	hub := i.fulfillHub()
	if ttl := strings.TrimSpace(routing.TurnFamilyTTL); ttl != "" {
		if d, err := time.ParseDuration(ttl); err == nil {
			hub.SetFamilyTTL(d)
			return
		}
	}
	hub.SetFamilyTTL(DefaultTurnFamilyTTL)
}

// evaluateToolFollowupRunSSE re-decides child/tool-loop RunSSE using tool_followup
// policy. PreferLocal → tool_followup_would_local; fulfill locally only when
// Path B has a tool codec (HasRunSSEToolCodec); otherwise origin.
func (i *Interceptor) evaluateToolFollowupRunSSE(w http.ResponseWriter, r *http.Request, body []byte, host, path string, start time.Time, log *slog.Logger) (bool, error) {
	parentTool := ""
	if r != nil {
		parentTool = r.Header.Get("X-Parent-Agent-Tool-Call-Id")
	}
	hub := i.fulfillHub()
	reqID := cursorrpc.ExtractRunSSERequestID(body)
	if reqID == "" {
		reqID = cursorrpc.RequestIDFromHeaders(r)
	}
	sess := ClientSessionKey(r)

	parentRoute := ""
	famRoot := ""
	famSrc := ""
	if mode, root, src, ok := hub.LookupTurnFamily(); ok {
		parentRoute = mode.String()
		famRoot = root
		famSrc = src
	}
	// Graph Bind/Lookup belt when TTL map is empty but this request/session is
	// already bound into a turn (no newest-cloud fallback — avoids cross-talk).
	if parentRoute == "" {
		g := hub.graph()
		if reqID != "" {
			if view, ok := g.Turn(reqID); ok && view.Route != "" {
				parentRoute = view.Route
				famRoot = view.ID
				if view.RootRequestID != "" {
					famRoot = view.RootRequestID
				}
				famSrc = view.Source
			}
		}
		if parentRoute == "" && sess != "" {
			if tid := g.TurnIDForSession(sess); tid != "" {
				if view, ok := g.Turn(tid); ok && view.Route != "" {
					parentRoute = view.Route
					famRoot = view.ID
					if view.RootRequestID != "" {
						famRoot = view.RootRequestID
					}
					famSrc = view.Source
				}
			}
		}
	}

	cfg := config.ToolFollowupConfig{}
	if i != nil {
		cfg = i.ToolFollowup
	}
	names := toolNamesForFollowup(r, body, cfg)
	verdict := router.DecideToolFollowup(cfg, router.ToolFollowupSignals{
		ParentRoute:   parentRoute,
		ToolNames:     names,
		ExplicitCloud: false,
		ExplicitLocal: false,
	})

	if famRoot != "" && reqID != "" && famRoot != reqID {
		hub.graph().BindRequest(famRoot, reqID)
	}
	if sess != "" && famRoot != "" {
		hub.graph().BindSession(famRoot, sess)
	}
	toolAttrs := map[string]string{
		"reason":       verdict.Reason,
		"prefer_local": fmt.Sprintf("%v", verdict.PreferLocal),
		"parent_route": parentRoute,
		"parent_tool":  parentTool,
		"family_src":   famSrc,
	}
	if len(names) > 0 {
		toolAttrs["tools"] = strings.Join(names, ",")
	}
	i.emitContextEvent(contextgraph.EventToolStarted, reqID, famRoot, "mitm", toolAttrs, sess)

	canFulfill := verdict.PreferLocal && i != nil && i.AgentRPCToolCodec &&
		cursorrpc.HasRunSSEToolCodec() && cursorrpc.HasRunSSETextCodec()
	log.Info("mitm runsse tool-loop re-decide",
		"host", host, "path", path,
		"corr_id", reqID,
		"parent_tool", parentTool,
		"parent_route", parentRoute,
		"tools", names,
		"prefer_local", verdict.PreferLocal,
		"reason", verdict.Reason,
		"followup_enabled", cfg.Enabled,
		"path_b_can_fulfill", canFulfill,
	)

	if i != nil && i.Metrics != nil {
		if verdict.PreferLocal {
			i.Metrics.IncAction("mitm", "tool_followup_would_local")
		} else {
			i.Metrics.IncAction("mitm", "tool_followup_origin")
		}
	}

	if canFulfill {
		// Opt-in Path B tool codec: Wait for a correlated local arm (short), then
		// CompleteLocal + WriteRunSSEToolResponse. Fail-soft to origin when no offer.
		offer, ok := hub.Wait(reqID, 80*time.Millisecond)
		if ok && offer != nil && offer.Local && offer.Request != nil && i.Harness != nil {
			req := offer.Request
			req.Model = api.NormalizeGatewayModel(req.Model)
			if req.Metadata.RequestID == "" {
				req.Metadata.RequestID = reqID
			}
			req.Metadata.Adapter = "cursor_runsse_tool_followup"
			chunks, err := i.Harness.CompleteLocal(r, req)
			if err == nil {
				if i.Metrics != nil {
					i.Metrics.IncAction("mitm", "tool_followup_local")
				}
				i.recordPathB(r, "local", "local", host, path, req.Model, req.Metadata.OriginalModel,
					"tool_followup:"+verdict.Reason, 0, reqID, start)
				i.emitContextEvent(contextgraph.EventToolFinished, reqID, famRoot, "local", map[string]string{
					"reason": verdict.Reason, "outcome": "local",
				}, sess)
				if werr := cursorrpc.WriteRunSSEToolResponse(w, chunks); werr != nil {
					log.Error("mitm runsse tool-loop encode failed", "corr_id", reqID, "err", werr)
					return true, werr
				}
				return true, nil
			}
			log.Info("mitm runsse tool-loop CompleteLocal failed → origin",
				"corr_id", reqID, "err", err)
		} else if i.CannedOnError {
			text := strings.TrimSpace(i.CannedText)
			if text == "" {
				text = DefaultCannedRunSSEText
			}
			if i.Metrics != nil {
				i.Metrics.IncAction("mitm", "tool_followup_local")
				i.Metrics.IncAction("mitm", "runsse_canned")
			}
			i.recordPathB(r, "canned", "local", host, path, "", "",
				"tool_followup_canned:"+verdict.Reason, 0, reqID, start)
			i.emitContextEvent(contextgraph.EventToolFinished, reqID, famRoot, "local", map[string]string{
				"reason": verdict.Reason, "outcome": "canned",
			}, sess)
			if werr := cursorrpc.WriteRunSSEToolResponse(w, cursorrpc.CannedCompletionChunks(text)); werr != nil {
				return true, werr
			}
			return true, nil
		}
		// Codec on but no local arm / CompleteLocal failed — fall through to origin.
	}

	if i != nil && i.Metrics != nil {
		i.Metrics.IncAction("mitm", "runsse_skip_tool_loop")
	}
	rule := "tool_followup:" + verdict.Reason
	if verdict.PreferLocal {
		rule = "tool_followup_would_local:" + verdict.Reason
	}
	route := "cloud"
	if strings.EqualFold(parentRoute, "local") && verdict.PreferLocal {
		route = "local"
	}
	i.recordPathB(r, "origin_passthrough", route, host, path, "", "",
		rule, 0, reqID, start)
	i.emitContextEvent(contextgraph.EventOriginPassthrough, reqID, famRoot, route, map[string]string{
		"rule": rule, "reason": verdict.Reason,
	}, sess)
	i.emitContextEvent(contextgraph.EventToolFinished, reqID, famRoot, "mitm", map[string]string{
		"reason": verdict.Reason, "outcome": "origin",
	}, sess)
	return false, nil
}

// toolNamesForFollowup prefers header hints, then scans the body for catalog names.
func toolNamesForFollowup(r *http.Request, body []byte, cfg config.ToolFollowupConfig) []string {
	if r != nil {
		for _, h := range []string{"X-Agent-Tool-Name", "X-Cursor-Tool-Name", "X-Glider-Tool-Name"} {
			if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
				parts := strings.Split(v, ",")
				out := make([]string, 0, len(parts))
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						out = append(out, p)
					}
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	catalog := make([]string, 0, len(cfg.LocalToolAllowlist)+len(cfg.CloudToolDenylist))
	catalog = append(catalog, cfg.LocalToolAllowlist...)
	catalog = append(catalog, cfg.CloudToolDenylist...)
	return scanToolNamesInBytes(body, catalog)
}

func scanToolNamesInBytes(body []byte, catalog []string) []string {
	if len(body) == 0 || len(catalog) == 0 {
		return nil
	}
	lower := bytes.ToLower(body)
	var out []string
	seen := map[string]struct{}{}
	for _, c := range catalog {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Skip glob patterns for body scan.
		if strings.ContainsAny(c, "*?[]") {
			continue
		}
		needle := []byte(strings.ToLower(c))
		if !bytes.Contains(lower, needle) {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// annotateComposerWrapupMeta stamps Path B sticky/wrap-up signals onto req so the
// first-class composer_wrapup_origin routing rule can match in DecideLocal.
func (i *Interceptor) annotateComposerWrapupMeta(req *backend.CompletionRequest, hub *AgentFulfillHub, ext *cursorrpc.BidiExtract, scan []byte, sess, corrID string) {
	if req == nil {
		return
	}
	src := ""
	if ext != nil {
		src = ext.Source
	}
	req.Metadata.ExtractSource = src
	if len(scan) > 0 {
		// Cap metadata copy; keywords appear early in chrome packs.
		capN := len(scan)
		if capN > 64<<10 {
			capN = 64 << 10
		}
		req.Metadata.WrapupScan = string(scan[:capN])
	}
	sticky := false
	if hub != nil {
		if mode, _, _, ok := hub.LookupTurnFamily(); ok && mode == StickyCloud {
			sticky = true
		}
		if !sticky {
			if _, _, stick := hub.ShouldStickyCloudOrigin(extUserText(ext), src, nil, scan); stick {
				sticky = true
			}
		}
		g := hub.graph()
		if g != nil {
			if corrID != "" && g.CloudTurnLive(corrID) {
				sticky = true
			}
			if sess != "" {
				if tid := g.TurnIDForSession(sess); tid != "" && g.CloudTurnLive(tid) {
					sticky = true
				}
				if route := g.SessionLastRoute(sess); route == "cloud" {
					req.Metadata.LastRouteCloud = true
				}
			}
			if _, _, _, live := g.LiveCloudFamily(); live {
				sticky = true
				req.Metadata.LastRouteCloud = true
			}
		}
	}
	req.Metadata.StickyCloudLive = sticky
}

func extUserText(ext *cursorrpc.BidiExtract) string {
	if ext == nil {
		return ""
	}
	return ext.UserText
}

// tryArmComposerWrapupOrigin ArmOrigins when wrap-up chrome matches or when a
// StickyCloud / last-cloud family is live for a non-fresh TipTap follow-on.
func (i *Interceptor) tryArmComposerWrapupOrigin(r *http.Request, req *backend.CompletionRequest, hub *AgentFulfillHub, ext *cursorrpc.BidiExtract, scan []byte, corrID, sess, host, path string) bool {
	if hub == nil || ext == nil {
		return false
	}
	userText := ext.UserText
	wrapup := router.MatchComposerWrapup(userText, scan) || IsSystemSummaryChrome(userText, scan) || IsTurnFollowOnBody(userText, scan)
	fresh := HasFreshUserTipTapTurn(userText, ext.Source, scan)
	sticky := req != nil && req.Metadata.StickyCloudLive
	lastCloud := req != nil && req.Metadata.LastRouteCloud
	chromeCrumb := !fresh && (strings.TrimSpace(userText) == "" || len(strings.TrimSpace(userText)) < 16 ||
		ext.Source == "printable_hint" || ext.Source == "section_fallback")

	if !(wrapup || (sticky && !fresh) || (lastCloud && !fresh && (wrapup || chromeCrumb))) {
		return false
	}
	// Never steal a live StickyLocal family's reply-summary / title inherit.
	if mode, _, _, ok := hub.LookupTurnFamily(); ok && mode == StickyLocal {
		return false
	}

	rule := router.ComposerWrapupRuleName
	metric := "bidi_composer_wrapup"
	if sticky && !wrapup {
		rule = "turn_family_cloud_summary"
		metric = "bidi_sticky_cloud_summary"
	}
	log := i.log()
	log.Info("mitm bidi composer_wrapup_origin → origin",
		"id", reqMetaID(req),
		"corr_id", corrID,
		"rule", rule,
		"wrapup", wrapup,
		"sticky_cloud", sticky,
		"last_route_cloud", lastCloud,
		"extract_source", ext.Source,
		"user_preview", truncateRunes(userText, 64),
	)
	hub.ArmOrigin(corrID)
	if sticky || lastCloud {
		// Keep family warm for subsequent chrome while graph still knows last cloud.
		if mode, root, _, ok := hub.LookupTurnFamily(); ok && mode == StickyCloud && root != "" {
			if corrID != "" && root != corrID {
				hub.graph().BindRequest(root, corrID)
			}
			if sess != "" {
				hub.graph().BindSession(root, sess)
			}
		} else if sess != "" {
			if tid := hub.graph().TurnIDForSession(sess); tid != "" {
				hub.graph().BindRequest(tid, corrID)
				hub.rehydrateCloudFamily(tid, "composer_wrapup")
			}
		}
	}
	model, orig, tokens := "", "", 0
	if req != nil {
		model, orig, tokens = req.Model, req.Metadata.OriginalModel, req.Metadata.EstimatedTokens
	}
	i.recordPathB(r, "origin_passthrough", "cloud", host, path, model, orig, rule, tokens, corrID, time.Now())
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", "bidi_decide_passthrough")
		i.Metrics.IncAction("mitm", metric)
		i.Metrics.IncAction("mitm", "bidi_composer_wrapup")
		if metric == "bidi_sticky_cloud_summary" {
			i.Metrics.IncAction("mitm", "bidi_sticky_cloud")
		}
	}
	i.emitContextEvent(contextgraph.EventSummaryRequested, corrID, "", "cloud", map[string]string{
		"rule": rule, "preview": truncateRunes(userText, 80),
	}, sess)
	i.emitContextEvent(contextgraph.EventOriginPassthrough, corrID, "", "cloud", map[string]string{
		"rule": rule,
	}, sess)
	return true
}

func reqMetaID(req *backend.CompletionRequest) string {
	if req == nil {
		return ""
	}
	return req.Metadata.RequestID
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

func requestHasCloudOverride(req *backend.CompletionRequest) bool {
	if req == nil {
		return false
	}
	for _, m := range req.Messages {
		if router.HasCloudOverride(m.Content) {
			return true
		}
	}
	return false
}

func requestHasLocalOverride(req *backend.CompletionRequest) bool {
	if req == nil {
		return false
	}
	for _, m := range req.Messages {
		if router.HasLocalOverride(m.Content) {
			return true
		}
	}
	return false
}

func (i *Interceptor) log() *slog.Logger {
	if i != nil && i.Log != nil {
		return i.Log
	}
	return slog.Default()
}

// logSkip records opaque / non-harness paths. Agent RPCs are Info so operators see why
// chats are not rerouted; control/telemetry stays Debug.
func (i *Interceptor) logSkip(log *slog.Logger, kind PathKind, host, path, method, contentType string) {
	action := "skip"
	msg := "mitm skip non-llm path"
	attrs := []any{"host", host, "path", path, "method", method, "kind", kind.String()}
	if contentType != "" {
		attrs = append(attrs, "content_type", contentType)
	}
	switch kind {
	case PathAgentRPC:
		action = "skip_agent_rpc"
		msg = "mitm skip agent rpc (opaque protobuf) → origin"
		log.Info(msg, attrs...)
	case PathCursorControl:
		action = "skip_control"
		msg = "mitm skip cursor control → origin"
		log.Debug(msg, attrs...)
	default:
		log.Debug(msg, attrs...)
	}
	if i.Metrics != nil {
		i.Metrics.IncAction("mitm", action)
	}
}

func (i *Interceptor) observe(action, host, path, model, orig, rule string, tokens int, start time.Time) {
	if i == nil || i.Metrics == nil {
		return
	}
	route := action
	if action == "origin_passthrough" {
		route = "cloud"
	}
	i.Metrics.Record(metrics.RequestRecord{
		ID:            fmt.Sprintf("mitm_%d", time.Now().UnixNano()),
		Mode:          "mitm",
		Action:        action,
		Route:         route,
		Model:         model,
		OriginalModel: orig,
		Host:          host,
		Path:          path,
		Rule:          rule,
		Tokens:        tokens,
		Latency:       time.Since(start),
	})
}

// recordPathB publishes an Overview request-log row for Path B cloud/local/canned outcomes
// so LOCAL/CLOUD % includes Agent turns (not only IncAction counters).
func (i *Interceptor) recordPathB(r *http.Request, action, route, host, path, model, orig, rule string, tokens int, id string, start time.Time) {
	if i == nil || i.Metrics == nil {
		return
	}
	if route == "" {
		route = action
		if action == "origin_passthrough" {
			route = "cloud"
		}
		if action == "canned" {
			route = "local"
		}
	}
	if id == "" {
		id = fmt.Sprintf("mitm_pathb_%d", time.Now().UnixNano())
	}
	clientSess := ""
	if r != nil {
		clientSess = ClientSessionKey(r)
	}
	rec := metrics.RequestRecord{
		ID:            id,
		Mode:          "mitm",
		Action:        action,
		Route:         route,
		Model:         model,
		OriginalModel: orig,
		Host:          host,
		Path:          path,
		Rule:          rule,
		Tokens:        tokens,
		Latency:       time.Since(start),
		ClientSession: clientSess,
	}
	i.Metrics.Record(rec)
}

// emitContextEvent appends to the orchestrator contextgraph (sticky + debug).
// Optional trailing sessionKeys bind connect/x-session into the turn index.
func (i *Interceptor) emitContextEvent(kind contextgraph.EventKind, requestID, turnID, actor string, attrs map[string]string, sessionKeys ...string) {
	if i == nil {
		return
	}
	hub := i.fulfillHub()
	g := hub.graph()
	if g == nil {
		return
	}
	if turnID == "" {
		turnID = requestID
	}
	sess := ""
	if len(sessionKeys) > 0 {
		sess = sessionKeys[0]
	}
	g.Append(contextgraph.Event{
		Kind:           kind,
		TurnID:         turnID,
		RequestID:      requestID,
		ConnectSession: sess,
		Actor:          actor,
		Attrs:          attrs,
	})
}

func writeChatSSE(w http.ResponseWriter, id, model string, chunks <-chan backend.CompletionChunk) error {
	return api.WriteChatSSE(w, id, model, chunks)
}

func writeChatJSON(w http.ResponseWriter, id, model string, chunks <-chan backend.CompletionChunk) error {
	return api.WriteChatJSON(w, id, model, chunks)
}

// PeekJSONField extracts a top-level string field without full unmarshal (debug helper).
func PeekJSONField(body []byte, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
