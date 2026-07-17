package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// T3.6.1 — Local failure triggers cloud fallback
func TestFallback_LocalFailureCloudSuccess(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    true,
		completeFn: errorComplete(errors.New("local crash")),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("from cloud"),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         newStubVRAM(),
		CloudBackend: "openai",
		CloudModel:   "gpt-4o",
	})

	decision := &backend.RoutingDecision{
		Target:      "local",
		BackendName: "ollama",
		Model:       "codellama:7b",
	}
	req := &backend.CompletionRequest{
		Model:    "codellama:7b",
		Messages: []backend.Message{{Role: "user", Content: "hi"}},
	}

	ch, err := exec.Execute(context.Background(), decision, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var content strings.Builder
	for c := range ch {
		content.WriteString(c.Content)
	}
	if content.String() != "from cloud" {
		t.Fatalf("content = %q, want from cloud", content.String())
	}
}

// T3.6.2 — Both local and cloud fail → error to caller
func TestFallback_BothFailReturns502(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    true,
		completeFn: errorComplete(errors.New("local fail")),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: errorComplete(errors.New("cloud fail")),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         newStubVRAM(),
		CloudBackend: "openai",
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{Model: "codellama:7b"}

	_, err := exec.Execute(context.Background(), decision, req)
	if err == nil {
		t.Fatal("expected error")
	}
	gw, ok := err.(*orchestrator.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gw.StatusCode != 502 {
		t.Fatalf("status = %d, want 502", gw.StatusCode)
	}
}

// T3.6.3 — Circuit breaker opens after repeated failures
func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	reg := backend.NewRegistry()
	attempts := 0
	local := &mockBackend{
		name:    "ollama",
		typ:     backend.BackendTypeLocal,
		healthy: true,
		completeFn: func(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
			attempts++
			return nil, errors.New("fail")
		},
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("cloud ok"),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:         reg,
		VRAM:             newStubVRAM(),
		CloudBackend:     "openai",
		FailureThreshold: 5,
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{Model: "codellama:7b"}

	for i := 0; i < 5; i++ {
		_, _ = exec.Execute(context.Background(), decision, req)
	}

	before := attempts
	_, err := exec.Execute(context.Background(), decision, req)
	if err != nil {
		t.Fatalf("6th request should succeed via cloud: %v", err)
	}
	if attempts != before {
		t.Fatalf("6th request hit local backend (attempts %d -> %d)", before, attempts)
	}
	if exec.Fallback().BreakerFor("ollama").State() != orchestrator.BreakerOpen {
		t.Fatal("breaker should be OPEN")
	}
}

// T3.6.4 — Circuit breaker half-open: probe after cooldown
func TestCircuitBreaker_HalfOpenProbe(t *testing.T) {
	cb := orchestrator.NewCircuitBreaker(5, 30*time.Millisecond)
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.State() != orchestrator.BreakerOpen {
		t.Fatalf("state = %v, want OPEN", cb.State())
	}
	if cb.Allow() {
		t.Fatal("should not allow before cooldown")
	}

	time.Sleep(35 * time.Millisecond)
	if !cb.Allow() {
		t.Fatal("should allow probe after cooldown (HALF-OPEN)")
	}
	if cb.State() != orchestrator.BreakerHalfOpen {
		t.Fatalf("state = %v, want HALF-OPEN", cb.State())
	}

	cb.RecordSuccess()
	if cb.State() != orchestrator.BreakerClosed {
		t.Fatalf("state = %v, want CLOSED after success", cb.State())
	}
}
