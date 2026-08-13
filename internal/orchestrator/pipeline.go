package orchestrator

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/google/uuid"
)

// The code returns ErrOriginPassthrough when the MITM mode selects a target
// that is not local. The MITM interceptor then changes this into a passthrough
// to the origin, which is the upstream server of Cursor.
var ErrOriginPassthrough = errors.New("origin passthrough")

// CompleteOptions controls harness behavior for gateway vs MITM.
type CompleteOptions struct {
	// OriginPassthrough maps non-local routing decisions to ErrOriginPassthrough
	// instead of executing cloud/BYOK backends. Used by Mode B (MITM).
	OriginPassthrough bool
}

// PipelineCompleter implements api.Completer. It does four steps in sequence:
// tokenize, route, transform and execute.
//
// How context comes to a local model. Refer to
// planning/routing_and_context.md §5.
//
//	Path B: ExtractBidiCompletionRequest gives one user message from the
//	        latest turn of TipTap. It has no tools and no full envelope.
//	        Then DecideLocal and CompleteLocal operate.
//	Path A: the routing uses the full body of the gateway. When Target is
//	        local, BoundLocalContext can make the content smaller, to the
//	        system message and the latest turn, before the execute step.
//
// StickyCloud and the refusal of a local model occur in MITM, before
// CompleteLocal. This pipeline never changes ArmOrigin.
type PipelineCompleter struct {
	Router      router.Router
	Executor    Executor
	Tokenizer   *transform.Tokenizer
	Transformer *transform.Transformer
	// TransformCfg drives BoundLocalContext for local fulfills (optional).
	TransformCfg config.TransformConfig
	MaxContext   int
	Metrics      *metrics.Collector
	ModelAliases map[string]string
	// Graph is optional; nil → no contextgraph emits (tests). Classifier / explicit
	// decisions emit EventRouteDecided without owning contextgraph internals.
	Graph *contextgraph.Store
	// Episodes is optional session episode ring (wired from main).
	Episodes *contextkit.Store
	// EpisodeSession is the default session key for RecordEpisode / inject (Glider run id).
	EpisodeSession string
}

func (p *PipelineCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return p.Handle(r, req, CompleteOptions{})
}

// CompleteLocal is the MITM entry: same harness as Complete, but non-local → ErrOriginPassthrough.
func (p *PipelineCompleter) CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return p.Handle(r, req, CompleteOptions{OriginPassthrough: true})
}

// DecideLocal runs alias → tokenize → route without executing a backend.
// Used by Path B Bidi extract so local intent can arm AgentFulfillHub for RunSSE.
func (p *PipelineCompleter) DecideLocal(r *http.Request, req *backend.CompletionRequest) (*backend.RoutingDecision, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	ApplyModelAlias(req, p.ModelAliases)
	if p.Tokenizer != nil {
		req.Metadata.EstimatedTokens = p.Tokenizer.EstimateRequestTokens(req)
	}
	if r == nil {
		return nil, fmt.Errorf("nil request context")
	}
	decision, err := p.Router.Route(r.Context(), req)
	if err != nil {
		p.emitOrchEvent(contextgraph.EventError, req, "", map[string]string{"where": "route", "err": err.Error()})
		return nil, err
	}
	if decision.BackendName == "" {
		decision.BackendName = ResolveBackendName(decision)
	}
	if decision.Model != "" {
		req.Model = decision.Model
	}
	if decision.Adapter != "" {
		req.Metadata.Adapter = decision.Adapter
	}
	p.emitRouteDecided(req, decision, "decide_local")
	return decision, nil
}

// Handle runs alias → tokenize → route → (optional origin passthrough) → transform → execute.
func (p *PipelineCompleter) Handle(r *http.Request, req *backend.CompletionRequest, opts CompleteOptions) (<-chan backend.CompletionChunk, error) {
	start := time.Now()
	mode := "gateway"
	if opts.OriginPassthrough {
		mode = "mitm"
	}
	host, path := "", ""
	if r != nil {
		host = r.Host
		if host == "" && r.URL != nil {
			host = r.URL.Host
		}
		if r.URL != nil {
			path = r.URL.Path
		}
	}

	ApplyModelAlias(req, p.ModelAliases)
	if p.Tokenizer != nil {
		req.Metadata.EstimatedTokens = p.Tokenizer.EstimateRequestTokens(req)
	}
	decision, err := p.Router.Route(r.Context(), req)
	if err != nil {
		p.record(req, mode, "error", "", host, path, nil, start)
		p.emitOrchEvent(contextgraph.EventError, req, "orch", map[string]string{"where": "route", "err": err.Error()})
		return nil, err
	}
	if decision.BackendName == "" {
		decision.BackendName = ResolveBackendName(decision)
	}
	if decision.Model != "" {
		req.Model = decision.Model
	}
	if decision.Adapter != "" {
		req.Metadata.Adapter = decision.Adapter
	}
	p.emitRouteDecided(req, decision, mode)

	if opts.OriginPassthrough && !IsLocalTarget(decision.Target) {
		p.record(req, mode, "origin_passthrough", decision.Target, host, path, decision, start)
		p.emitOrchEvent(contextgraph.EventOriginPassthrough, req, decision.Target, map[string]string{
			"rule": decision.RuleName, "route": decision.Target, "source": mode,
		})
		return nil, ErrOriginPassthrough
	}

	// Limit the context for a local fulfillment only. This is the large history of
	// Path A, or StreamChat. The extraction of one turn in Path B does not change,
	// and a sticky origin never arrives here.
	//
	// Put the compressed episodes first, and not the dumps of TipTap. Then limit
	// the content to the latest turn.
	if IsLocalTarget(decision.Target) {
		if p.Episodes != nil && p.TransformCfg.LocalEpisodeCount > 0 {
			sess := p.episodeSessionKey(req)
			eps := p.Episodes.RecentEpisodes(sess, p.TransformCfg.LocalEpisodeCount)
			req = transform.InjectEpisodeContext(req, eps, p.TransformCfg)
		}
		req = transform.BoundLocalContext(req, p.TransformCfg)
	}

	if p.Transformer != nil {
		maxCtx := p.MaxContext
		if maxCtx <= 0 {
			maxCtx = 8192
		}
		req = p.Transformer.Apply(req, maxCtx)
	}

	ch, err := p.Executor.Execute(r.Context(), decision, req)
	if err != nil {
		p.record(req, mode, "error", decision.Target, host, path, decision, start)
		p.emitOrchEvent(contextgraph.EventError, req, decision.Target, map[string]string{
			"where": "execute", "err": err.Error(), "rule": decision.RuleName,
		})
		return nil, err
	}

	if p.Metrics == nil {
		if IsLocalTarget(decision.Target) {
			p.emitOrchEvent(contextgraph.EventFulfilledLocal, req, "local", map[string]string{
				"rule": decision.RuleName, "source": mode,
			})
			// Put the stream in a wrapper. Therefore the code can record the episode when
			// the stream completes.
			return p.wrapRecordEpisode(ch, req, decision, mode), nil
		}
		return ch, nil
	}

	out := make(chan backend.CompletionChunk, 16)
	go func() {
		defer close(out)
		var buf strings.Builder
		for chunk := range ch {
			if chunk.Content != "" {
				buf.WriteString(chunk.Content)
			}
			out <- chunk
		}
		action := "local"
		if !IsLocalTarget(decision.Target) {
			action = "cloud"
		}
		p.record(req, mode, action, decision.Target, host, path, decision, start)
		if IsLocalTarget(decision.Target) {
			p.emitOrchEvent(contextgraph.EventFulfilledLocal, req, "local", map[string]string{
				"rule": decision.RuleName, "source": mode,
			})
			p.recordLocalEpisode(req, decision, buf.String())
		}
	}()
	return out, nil
}

func (p *PipelineCompleter) wrapRecordEpisode(ch <-chan backend.CompletionChunk, req *backend.CompletionRequest, decision *backend.RoutingDecision, mode string) <-chan backend.CompletionChunk {
	out := make(chan backend.CompletionChunk, 16)
	go func() {
		defer close(out)
		var buf strings.Builder
		for chunk := range ch {
			if chunk.Content != "" {
				buf.WriteString(chunk.Content)
			}
			out <- chunk
		}
		p.recordLocalEpisode(req, decision, buf.String())
	}()
	return out
}

func (p *PipelineCompleter) episodeSessionKey(req *backend.CompletionRequest) string {
	if p != nil && p.EpisodeSession != "" {
		return p.EpisodeSession
	}
	if req != nil && req.Metadata.RequestID != "" {
		return req.Metadata.RequestID
	}
	return "default"
}

func (p *PipelineCompleter) recordLocalEpisode(req *backend.CompletionRequest, decision *backend.RoutingDecision, text string) {
	if p == nil || p.Episodes == nil || req == nil {
		return
	}
	summary := strings.TrimSpace(text)
	if len(summary) > 512 {
		summary = summary[:512]
	}
	if summary == "" {
		summary = "(empty local fulfill)"
	}
	epID := uuid.New().String()
	reqID := req.Metadata.RequestID
	turnID := reqID
	if g := p.graph(); g != nil && reqID != "" {
		if tid := g.TurnIDForRequest(reqID); tid != "" {
			turnID = tid
		}
	}
	rule, reason, role := "", "", "local"
	if decision != nil {
		rule = decision.RuleName
		reason = decision.Reason
		if decision.Role != "" {
			role = decision.Role
		}
	}
	tokens := len(summary) / 4
	if req.Metadata.EstimatedTokens > 0 {
		tokens = req.Metadata.EstimatedTokens
	}
	sess := p.episodeSessionKey(req)
	p.Episodes.RecordEpisode(sess, contextkit.Episode{
		ID:      epID,
		TurnID:  turnID,
		Summary: summary,
		Tokens:  tokens,
		Model:   req.Model,
		Rule:    rule,
		Reason:  reason,
		Role:    role,
	})
	if g := p.graph(); g != nil {
		g.RecordEpisodeMerged(turnID, reqID, epID, map[string]string{
			"rule": rule, "model": req.Model, "source": "fulfill",
		})
	}
}

func (p *PipelineCompleter) graph() *contextgraph.Store {
	if p != nil && p.Graph != nil {
		return p.Graph
	}
	return nil
}

// emitRouteDecided writes the decisions of the classifier, of an explicit
// command and of a script into the event log of contextgraph, when a person
// connected Graph. It only appends. It does nothing when Graph is nil.
// Therefore a test, or a caller with no store, writes nothing to the
// graph.
func (p *PipelineCompleter) emitRouteDecided(req *backend.CompletionRequest, decision *backend.RoutingDecision, source string) {
	if p == nil || decision == nil || req == nil {
		return
	}
	g := p.graph()
	if g == nil {
		return
	}
	attrs := map[string]string{
		"route":  decision.Target,
		"source": source,
		"rule":   decision.RuleName,
	}
	if decision.Reason != "" {
		attrs["reason"] = decision.Reason
	}
	if decision.Role != "" {
		attrs["role"] = decision.Role
	}
	if decision.Model != "" {
		attrs["model"] = decision.Model
	}
	reqID := req.Metadata.RequestID
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventRouteDecided,
		TurnID:    reqID,
		RequestID: reqID,
		Actor:     decision.Target,
		Attrs:     attrs,
	})
}

func (p *PipelineCompleter) emitOrchEvent(kind contextgraph.EventKind, req *backend.CompletionRequest, actor string, attrs map[string]string) {
	if p == nil || req == nil {
		return
	}
	reqID := req.Metadata.RequestID
	if reqID == "" {
		return
	}
	p.graph().Append(contextgraph.Event{
		Kind:      kind,
		TurnID:    reqID,
		RequestID: reqID,
		Actor:     actor,
		Attrs:     attrs,
	})
}

func (p *PipelineCompleter) record(req *backend.CompletionRequest, mode, action, route, host, path string, decision *backend.RoutingDecision, start time.Time) {
	if p.Metrics == nil || req == nil {
		return
	}
	rule, reason, role := "", "", ""
	if decision != nil {
		rule = decision.RuleName
		reason = decision.Reason
		role = decision.Role
	}
	model := req.Model
	orig := req.Metadata.OriginalModel
	tokens := req.Metadata.EstimatedTokens
	id := req.Metadata.RequestID
	p.Metrics.Record(metrics.RequestRecord{
		ID:            id,
		Mode:          mode,
		Action:        action,
		Route:         route,
		Model:         model,
		OriginalModel: orig,
		Host:          host,
		Path:          path,
		Rule:          rule,
		Reason:        reason,
		Role:          role,
		Tokens:        tokens,
		Latency:       time.Since(start),
	})
}

// IsLocalTarget reports whether a routing target should be fulfilled locally.
func IsLocalTarget(target string) bool {
	return target == "local"
}

// ApplyModelAlias changes req.Model when a person configured an alias. The
// code keeps OriginalModel in the metadata when that field is empty.
func ApplyModelAlias(req *backend.CompletionRequest, aliases map[string]string) {
	if req == nil || len(aliases) == 0 || req.Model == "" {
		return
	}
	if mapped, ok := aliases[req.Model]; ok && mapped != "" {
		if req.Metadata.OriginalModel == "" {
			req.Metadata.OriginalModel = req.Model
		}
		req.Model = mapped
	}
}

// ResolveBackendName fills BackendName when a rule only set Target.
func ResolveBackendName(d *backend.RoutingDecision) string {
	if d.BackendName != "" {
		return d.BackendName
	}
	if d.Target == "cloud" {
		return "openai"
	}
	return "ollama"
}

// DecisionSummary is a short observability string.
func DecisionSummary(d *backend.RoutingDecision) string {
	return fmt.Sprintf("%s/%s via %s", d.Target, d.Model, d.RuleName)
}
