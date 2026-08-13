package orchestrator_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
)

type recordExecutor struct {
	n        int
	lastTgt  string
	lastName string
	lastReq  *backend.CompletionRequest
}

func (e *recordExecutor) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	e.n++
	e.lastTgt = decision.Target
	e.lastName = decision.RuleName
	e.lastReq = req
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: "ok", FinishReason: "stop", Model: req.Model}
	close(ch)
	return ch, nil
}

func TestPipelineCompleteGatewayExecutesCloud(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ch, err := pc.Complete(r, req)
	if err != nil {
		t.Fatal(err)
	}
	if exec.n != 1 || exec.lastTgt != "cloud" {
		t.Fatalf("gateway must execute cloud: n=%d tgt=%s", exec.n, exec.lastTgt)
	}
	for range ch {
	}
}

func TestPipelineCompleteLocalMITMPassthrough(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err = pc.CompleteLocal(r, req)
	if !errors.Is(err, orchestrator.ErrOriginPassthrough) {
		t.Fatalf("want ErrOriginPassthrough, got %v", err)
	}
	if exec.n != 0 {
		t.Fatal("MITM cloud must not execute backends")
	}
}

func TestPipelineCompleteLocalMITMFulfillsLocal(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ch, err := pc.CompleteLocal(r, req)
	if err != nil {
		t.Fatal(err)
	}
	if exec.n != 1 || exec.lastTgt != "local" {
		t.Fatalf("MITM local must execute: n=%d tgt=%s", exec.n, exec.lastTgt)
	}
	for range ch {
	}
}

func TestPipelineRecordsEpisodeOnLocalFulfill(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := contextgraph.New("")
	store := contextkit.NewStore(8)
	pc := &orchestrator.PipelineCompleter{
		Router:         engine,
		Executor:       &recordExecutor{},
		Graph:          g,
		Episodes:       store,
		EpisodeSession: "test-run",
	}
	req := &backend.CompletionRequest{
		Model: "codellama:7b",
		Messages: []backend.Message{
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "old reply"},
			{Role: "user", Content: "rename x"},
		},
		Metadata: backend.RequestMetadata{RequestID: "req-ep-1"},
	}
	pc.TransformCfg = config.TransformConfig{
		LocalContext:      "latest_turn",
		LocalEpisodeCount: 0,
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ch, err := pc.Complete(r, req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	// Allow goroutine to finish episode record.
	time.Sleep(20 * time.Millisecond)
	eps := store.RecentEpisodes("test-run", 5)
	if len(eps) != 1 {
		t.Fatalf("episodes=%d want 1", len(eps))
	}
	evs := g.RecentEvents(20)
	found := false
	for _, e := range evs {
		if e.Kind == contextgraph.EventEpisodeMerged {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing EpisodeMerged in %+v", evs)
	}
}


func TestPipelineTokenizeBeforeRoute(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Overflow", Priority: 10,
				Trigger: config.TriggerConfig{Type: "context_size", Operator: ">", Value: 50},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Small", Priority: 5,
				Trigger: config.TriggerConfig{Type: "context_size", Operator: "<=", Value: 50},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec, Tokenizer: tok}

	small := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if _, err := pc.Complete(r, small); err != nil {
		t.Fatal(err)
	}
	if exec.lastName != "Small" {
		t.Fatalf("rule=%s, want Small", exec.lastName)
	}

	large := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: strings.Repeat("word ", 200)}},
	}
	if _, err := pc.Complete(r, large); err != nil {
		t.Fatal(err)
	}
	if exec.lastName != "Overflow" {
		t.Fatalf("rule=%s, want Overflow", exec.lastName)
	}
}

func TestIsLocalTarget(t *testing.T) {
	if !orchestrator.IsLocalTarget("local") {
		t.Fatal("local")
	}
	if orchestrator.IsLocalTarget("cloud") {
		t.Fatal("cloud")
	}
}

func TestPipelineLocalBoundsMegaContext(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{
		Router:       engine,
		Executor:     exec,
		TransformCfg: config.TransformConfig{LocalContext: "latest_turn", LocalSystemMaxChars: 4000},
	}
	req := &backend.CompletionRequest{
		Model: "codellama:7b",
		Messages: []backend.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "old history"},
			{Role: "assistant", Content: "old reply"},
			{Role: "user", Content: "rename foo"},
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ch, err := pc.Complete(r, req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if exec.lastReq == nil || len(exec.lastReq.Messages) != 2 {
		t.Fatalf("local should get system+latest user, got %+v", exec.lastReq)
	}
	if exec.lastReq.Messages[1].Content != "rename foo" {
		t.Fatalf("latest turn lost: %+v", exec.lastReq.Messages)
	}
}

func TestPipelineCloudKeepsFullContext(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{
		Router:       engine,
		Executor:     exec,
		TransformCfg: config.TransformConfig{LocalContext: "latest_turn"},
	}
	req := &backend.CompletionRequest{
		Model: "gpt-4o",
		Messages: []backend.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "old"},
			{Role: "user", Content: "new"},
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ch, err := pc.Complete(r, req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if exec.lastReq == nil || len(exec.lastReq.Messages) != 3 {
		t.Fatalf("cloud must keep full history, got %+v", exec.lastReq)
	}
}

func TestPipelineRecordsMITMObservability(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Default Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)
	pc := &orchestrator.PipelineCompleter{
		Router:   engine,
		Executor: &recordExecutor{},
		Metrics:  metrics.NewCollector(bus),
	}
	req := &backend.CompletionRequest{
		Model:    "claude-sonnet",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
		Metadata: backend.RequestMetadata{RequestID: "obs1", OriginalModel: "claude-sonnet"},
	}
	r := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions", nil)
	r.Host = "api2.cursor.sh"
	_, err = pc.CompleteLocal(r, req)
	if !errors.Is(err, orchestrator.ErrOriginPassthrough) {
		t.Fatalf("want ErrOriginPassthrough, got %v", err)
	}
	select {
	case ev := <-ch:
		data, ok := ev.Data.(metrics.RequestEventData)
		if !ok {
			t.Fatalf("data type %T", ev.Data)
		}
		if data.Mode != "mitm" || data.Action != "origin_passthrough" {
			t.Fatalf("mode/action=%s/%s", data.Mode, data.Action)
		}
		if data.Host != "api2.cursor.sh" || data.Path != "/v1/chat/completions" {
			t.Fatalf("host/path=%s %s", data.Host, data.Path)
		}
		if data.Rule != "Default Cloud" || data.OriginalModel != "claude-sonnet" {
			t.Fatalf("rule/orig=%s %s", data.Rule, data.OriginalModel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for metrics event")
	}
}

func TestPipelineDecideLocal(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Always Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}
	r := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/x", nil)
	d, err := pc.DecideLocal(r, req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Target != "local" || d.Model != "codellama:7b" {
		t.Fatalf("%+v", d)
	}
	if exec.n != 0 {
		t.Fatal("DecideLocal must not execute")
	}
}

func TestPipelineCloudOverrideNeverLocal(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:            true,
			LocalModel:         "codellama:7b",
			SmallLocalPriority: 200,
		},
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &recordExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/v1/chat/completions", nil)

	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "Composer\n/cloud rename foo to bar"}},
	}
	_, err = pc.CompleteLocal(r, req)
	if !errors.Is(err, orchestrator.ErrOriginPassthrough) {
		t.Fatalf("CompleteLocal err=%v want ErrOriginPassthrough", err)
	}
	if exec.n != 0 {
		t.Fatal("must not execute local backend for /cloud")
	}

	req2 := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "/cloud rename foo to bar"}},
	}
	ch, err := pc.Complete(r, req2)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if exec.n != 1 || exec.lastTgt != "cloud" || exec.lastName != "Explicit Cloud" {
		t.Fatalf("gateway Complete: n=%d tgt=%q rule=%q", exec.n, exec.lastTgt, exec.lastName)
	}
}

func TestPipelineEmitsContextGraphRouteDecided(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{Enabled: true, LocalModel: "codellama:7b"},
		Rules: []config.RuleConfig{{
			Name: "Default Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := contextgraph.New("")
	pc := &orchestrator.PipelineCompleter{
		Router:   engine,
		Executor: &recordExecutor{},
		Graph:    g,
	}
	r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/v1/chat/completions", nil)
	req := &backend.CompletionRequest{
		Model:    "gpt-4o",
		Messages: []backend.Message{{Role: "user", Content: "rename foo to bar"}},
		Metadata: backend.RequestMetadata{RequestID: "req-class-1"},
	}
	ch, err := pc.Complete(r, req)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	view, ok := g.Turn("req-class-1")
	if !ok {
		t.Fatal("expected contextgraph turn")
	}
	if view.Route != "local" {
		t.Fatalf("route=%q want local", view.Route)
	}
	found := false
	for _, ev := range view.Events {
		if ev.Kind == contextgraph.EventRouteDecided && ev.Attrs["reason"] == "small_offload" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing RouteDecided small_offload in %+v", view.Events)
	}
}
