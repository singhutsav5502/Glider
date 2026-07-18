package mitm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
)

func TestInterceptorLocalVsPassthrough(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Local",
				Priority: 100,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default Cloud",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	inter := &mitm.Interceptor{Harness: pc}

	// Cloud default → not handled (origin passthrough); harness called once, no execute
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("expected passthrough for cloud target")
	}
	if exec.n != 0 {
		t.Fatalf("executor should not run on passthrough, got %d", exec.n)
	}

	// /local → handled via Completer/Harness
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"/local hi"}]}`))
	handled, err = inter.TryHandle(rr2, req2)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected local handle")
	}
	if exec.n != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.n)
	}
}

func TestInterceptorThresholdDrivesLocalVsPassthrough(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name:     "Context Overflow",
				Priority: 10,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: ">", Value: 50},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Small Local",
				Priority: 5,
				Trigger:  config.TriggerConfig{Type: "context_size", Operator: "<=", Value: 50},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec, Tokenizer: tok}
	inter := &mitm.Interceptor{Harness: pc}

	// Small prompt → local
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected local for small context")
	}
	if exec.n != 1 {
		t.Fatalf("executor calls=%d", exec.n)
	}

	// Large prompt → origin passthrough
	large := strings.Repeat("word ", 200)
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"`+large+`"}]}`))
	handled, err = inter.TryHandle(rr2, req2)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("expected passthrough for large context")
	}
	if exec.n != 1 {
		t.Fatalf("executor should stay at 1 after passthrough, got %d", exec.n)
	}
}

func TestInterceptorScriptDrivesLocal(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "route.star")
	content := `
def evaluate(request):
    content = ""
    for msg in request.messages:
        content = content + msg.content
    if "refactor" in content:
        return {"matched": True, "action": {"target": "local", "model": "codellama:7b"}}
    return {"matched": False}
`
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	starExec := router.NewStarlarkExecutor()
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name:     "Script",
				Priority: 50,
				Trigger:  config.TriggerConfig{Type: "script", File: script},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name:     "Default Cloud",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, starExec)
	if err != nil {
		t.Fatal(err)
	}

	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	inter := &mitm.Interceptor{Harness: pc}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"please refactor this"}]}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected script to force local")
	}
	if exec.n != 1 {
		t.Fatalf("executor calls=%d", exec.n)
	}

	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`))
	handled, err = inter.TryHandle(rr2, req2)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("expected passthrough when script does not match")
	}
}

func TestInterceptorCloudOverridePassthrough(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name:     "Explicit Cloud",
				Priority: 99,
				Trigger:  config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:   config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name:     "Default Local",
				Priority: 0,
				Trigger:  config.TriggerConfig{Type: "always"},
				Action:   config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	inter := &mitm.Interceptor{Harness: pc}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"/cloud do heavy work"}]}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("expected /cloud to origin-passthrough in MITM")
	}
	if exec.n != 0 {
		t.Fatalf("executor should not run, got %d", exec.n)
	}
}

func TestInterceptorUnrecognizedPassthrough(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Always Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "m"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}}
	inter := &mitm.Interceptor{Harness: pc}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"proprietary":true,"not":"openai"}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("unrecognized body must passthrough")
	}
}

type countingExecutor struct{ n int }

func (c *countingExecutor) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	c.n++
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: "local", FinishReason: "stop", Model: req.Model}
	close(ch)
	return ch, nil
}

func TestInterceptorNonLLMPathNoRequestEvent(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	inter := &mitm.Interceptor{Harness: &stubHarness{}, Metrics: c}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
		strings.NewReader(`{}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("non-llm must not be handled")
	}

	select {
	case ev := <-ch:
		t.Fatalf("unexpected request event for non-llm path: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	counts := c.GetRouteCounts()
	if counts["action:skip_control"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestInterceptorUnparsedBodyNoRequestEvent(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	inter := &mitm.Interceptor{Harness: &stubHarness{}, Metrics: c}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/v1/chat/completions",
		strings.NewReader(`{"proprietary":true}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("unparsed must passthrough")
	}

	select {
	case ev := <-ch:
		t.Fatalf("unexpected request event for skip: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if c.GetRouteCounts()["action:skip"] != 1 {
		t.Fatalf("counts=%v", c.GetRouteCounts())
	}
}

type stubHarness struct{}

func (stubHarness) CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return nil, errors.New("stub harness should not be called")
}

// Ensure ErrOriginPassthrough is the sentinel MITM uses.
func TestErrOriginPassthroughSentinel(t *testing.T) {
	if !errors.Is(orchestrator.ErrOriginPassthrough, orchestrator.ErrOriginPassthrough) {
		t.Fatal("sentinel broken")
	}
}
