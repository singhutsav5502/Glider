package bench_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/config"
)

type instantBackend struct{}

func (instantBackend) Name() string              { return "instant" }
func (instantBackend) Type() backend.BackendType { return backend.BackendTypeLocal }
func (instantBackend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: "ok", FinishReason: "stop"}
	close(ch)
	return ch, nil
}

// T5.2.1 — Proxy passthrough overhead (informational benchmark)
func BenchmarkProxyPassthrough(b *testing.B) {
	reg := backend.NewRegistry()
	_ = reg.Register(instantBackend{})
	completer := &orchestrator.PassthroughCompleter{Registry: reg, BackendName: "instant", Model: "m"}
	hs := httptest.NewServer(api.NewServer("", &api.Handlers{Completer: completer}).Handler())
	defer hs.Close()

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	b.ResetTimer()
	var total time.Duration
	for i := 0; i < b.N; i++ {
		start := time.Now()
		resp, err := http.Post(hs.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		total += time.Since(start)
	}
	b.ReportMetric(float64(total.Microseconds())/float64(b.N)/1000.0, "ms/op_wall")
}

// T5.2.2 — Rule evaluation without Starlark
func BenchmarkRuleEvaluation(b *testing.B) {
	cfg := config.RoutingConfig{Rules: []config.RuleConfig{
		{Name: "e", Priority: 100, Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}}, Action: config.ActionConfig{Target: "local", Model: "m"}},
		{Name: "r", Priority: 50, Trigger: config.TriggerConfig{Type: "regex", Pattern: `(?i)\brefactor\b`}, Action: config.ActionConfig{Target: "local", Model: "m"}},
		{Name: "c", Priority: 10, Trigger: config.TriggerConfig{Type: "context_size", Operator: ">", Value: 8000}, Action: config.ActionConfig{Target: "cloud", Model: "x"}},
		{Name: "d", Priority: 0, Trigger: config.TriggerConfig{Type: "always"}, Action: config.ActionConfig{Target: "local", Model: "m"}},
	}}
	engine, err := router.NewEngineFromConfig(cfg, router.NewStarlarkExecutor())
	if err != nil {
		b.Fatal(err)
	}
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "Please refactor this class"}},
		Metadata: backend.RequestMetadata{EstimatedTokens: 100},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := engine.Route(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
