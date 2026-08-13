package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// T3.7.1 — Unhealthy backend is skipped during routing
func TestHealth_UnhealthyBackendSkipped(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    false,
		completeFn: chunkStream("local"),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("cloud"),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         newStubVRAM(),
		CloudBackend: "openai",
		IsHealthy:    orchestrator.DefaultHealthCheck(reg),
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{Model: "codellama:7b"}

	ch, err := exec.Execute(context.Background(), decision, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var content strings.Builder
	for c := range ch {
		content.WriteString(c.Content)
	}
	if content.String() != "cloud" {
		t.Fatalf("content = %q, want cloud (unhealthy local skipped)", content.String())
	}
}

// Unhealthy local that answers Ping becomes usable (Ollama-up-after-start case).
func TestHealth_ReprobeRecoversAfterPing(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    false,
		completeFn: chunkStream("local-ok"),
	}
	local.pingFn = func() error {
		local.healthy = true
		return nil
	}
	_ = reg.Register(local)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:  reg,
		VRAM:      newStubVRAM(),
		IsHealthy: orchestrator.DefaultHealthCheck(reg),
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{Model: "codellama:7b"}

	ch, err := exec.Execute(context.Background(), decision, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var content strings.Builder
	for c := range ch {
		content.WriteString(c.Content)
	}
	if content.String() != "local-ok" {
		t.Fatalf("content = %q, want local-ok after Ping reprobe", content.String())
	}
	if !local.IsHealthy() {
		t.Fatal("expected healthy after successful Ping")
	}
}

// T3.7.2 — Cloud rate limiter enforces RPM
func TestRateLimiter_EnforcesRPM(t *testing.T) {
	limiter := orchestrator.NewCloudRateLimiter(10)
	for i := 0; i < 10; i++ {
		if err := limiter.Allow(); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	err := limiter.Allow()
	if err == nil {
		t.Fatal("11th request should be blocked")
	}
	if _, ok := err.(*orchestrator.RateLimitExceededError); !ok {
		t.Fatalf("expected RateLimitExceededError, got %T", err)
	}
}

// T3.7.3 — Budget cap prevents cloud requests
func TestBudget_CapPreventsCloud(t *testing.T) {
	budget := orchestrator.NewBudgetTracker(50.0, func(tokens int) float64 {
		return 0.20
	})
	budget.SetSpent(49.90)

	err := budget.Allow(1000)
	if err == nil {
		t.Fatal("expected budget error")
	}
	be, ok := err.(*orchestrator.BudgetExceededError)
	if !ok {
		t.Fatalf("expected BudgetExceededError, got %T", err)
	}
	if be.Cap != 50.0 || be.Spent != 49.90 {
		t.Fatalf("unexpected budget error: %+v", be)
	}
}

func TestBudget_BlocksCloudRouting(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    true,
		completeFn: errorComplete(errors.New("local down")),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("cloud"),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	budget := orchestrator.NewBudgetTracker(50.0, func(tokens int) float64 { return 0.20 })
	budget.SetSpent(49.90)

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         newStubVRAM(),
		CloudBackend: "openai",
		Budget:       budget,
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{
		Model:    "codellama:7b",
		Metadata: backend.RequestMetadata{EstimatedTokens: 1000},
	}

	_, err := exec.Execute(context.Background(), decision, req)
	if err == nil {
		t.Fatal("expected budget error")
	}
	if _, ok := err.(*orchestrator.BudgetExceededError); !ok {
		t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
	}
}

func TestRateLimiter_BlocksCloudRouting(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:       "ollama",
		typ:        backend.BackendTypeLocal,
		healthy:    true,
		completeFn: errorComplete(errors.New("local down")),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("cloud"),
	}
	_ = reg.Register(local)
	_ = reg.Register(cloud)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	limiter := orchestrator.NewCloudRateLimiter(10)
	for i := 0; i < 10; i++ {
		_ = limiter.Allow()
	}

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         newStubVRAM(),
		CloudBackend: "openai",
		RateLimiter:  limiter,
	})

	decision := &backend.RoutingDecision{Target: "local", BackendName: "ollama", Model: "codellama:7b"}
	req := &backend.CompletionRequest{Model: "codellama:7b"}

	_, err := exec.Execute(context.Background(), decision, req)
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if _, ok := err.(*orchestrator.RateLimitExceededError); !ok {
		t.Fatalf("expected RateLimitExceededError, got %T: %v", err, err)
	}
}
