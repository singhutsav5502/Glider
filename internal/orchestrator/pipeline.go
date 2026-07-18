package orchestrator

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
)

// ErrOriginPassthrough is returned when MITM mode routes to a non-local target.
// The MITM interceptor converts this into an origin (Cursor upstream) passthrough.
var ErrOriginPassthrough = errors.New("origin passthrough")

// CompleteOptions controls harness behavior for gateway vs MITM.
type CompleteOptions struct {
	// OriginPassthrough maps non-local routing decisions to ErrOriginPassthrough
	// instead of executing cloud/BYOK backends. Used by Mode B (MITM).
	OriginPassthrough bool
}

// PipelineCompleter implements api.Completer: tokenize → route → transform → execute.
type PipelineCompleter struct {
	Router       router.Router
	Executor     Executor
	Tokenizer    *transform.Tokenizer
	Transformer  *transform.Transformer
	MaxContext   int
	Metrics      *metrics.Collector
	ModelAliases map[string]string
}

func (p *PipelineCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return p.Handle(r, req, CompleteOptions{})
}

// CompleteLocal is the MITM entry: same harness as Complete, but non-local → ErrOriginPassthrough.
func (p *PipelineCompleter) CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return p.Handle(r, req, CompleteOptions{OriginPassthrough: true})
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
		p.record(req, mode, "error", "", host, path, "", start)
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

	if opts.OriginPassthrough && !IsLocalTarget(decision.Target) {
		p.record(req, mode, "origin_passthrough", decision.Target, host, path, decision.RuleName, start)
		return nil, ErrOriginPassthrough
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
		p.record(req, mode, "error", decision.Target, host, path, decision.RuleName, start)
		return nil, err
	}

	if p.Metrics == nil {
		return ch, nil
	}

	out := make(chan backend.CompletionChunk, 16)
	go func() {
		defer close(out)
		for chunk := range ch {
			out <- chunk
		}
		action := "local"
		if !IsLocalTarget(decision.Target) {
			action = "cloud"
		}
		p.record(req, mode, action, decision.Target, host, path, decision.RuleName, start)
	}()
	return out, nil
}

func (p *PipelineCompleter) record(req *backend.CompletionRequest, mode, action, route, host, path, rule string, start time.Time) {
	if p.Metrics == nil || req == nil {
		return
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
		Tokens:        tokens,
		Latency:       time.Since(start),
	})
}

// IsLocalTarget reports whether a routing target should be fulfilled locally.
func IsLocalTarget(target string) bool {
	return target == "local"
}

// ApplyModelAlias rewrites req.Model when an alias is configured.
// OriginalModel is preserved in metadata when empty.
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
