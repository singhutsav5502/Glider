package orchestrator

import (
	"fmt"
	"net/http"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
)

// PipelineCompleter implements api.Completer: tokenize → route → transform → execute.
type PipelineCompleter struct {
	Router      router.Router
	Executor    Executor
	Tokenizer   *transform.Tokenizer
	Transformer *transform.Transformer
	MaxContext  int
	Metrics     *metrics.Collector
}

func (p *PipelineCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	start := time.Now()
	if p.Tokenizer != nil {
		req.Metadata.EstimatedTokens = p.Tokenizer.EstimateRequestTokens(req)
	}
	decision, err := p.Router.Route(r.Context(), req)
	if err != nil {
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

	if p.Transformer != nil {
		maxCtx := p.MaxContext
		if maxCtx <= 0 {
			maxCtx = 8192
		}
		req = p.Transformer.Apply(req, maxCtx)
	}

	ch, err := p.Executor.Execute(r.Context(), decision, req)
	if err != nil {
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
		p.Metrics.Record(metrics.RequestRecord{
			ID:      req.Metadata.RequestID,
			Route:   decision.Target,
			Model:   decision.Model,
			Tokens:  req.Metadata.EstimatedTokens,
			Latency: time.Since(start),
		})
	}()
	return out, nil
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
