package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// T3.4.1 — COLD → LOADING → WARM transition
func TestModelLifecycle_ColdToWarm(t *testing.T) {
	reg := backend.NewRegistry()
	ollama := &mockBackend{name: "ollama", typ: backend.BackendTypeLocal, healthy: true}
	_ = reg.Register(ollama)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	lc := orchestrator.NewModelLifecycle(reg, newStubVRAM(), time.Minute)
	ctx := context.Background()

	state, _ := lc.State("codellama:7b")
	if state != backend.ModelStateCold {
		t.Fatalf("initial state = %s, want COLD", state)
	}

	if err := lc.EnsureWarm(ctx, "codellama:7b"); err != nil {
		t.Fatalf("EnsureWarm: %v", err)
	}

	state, _ = lc.State("codellama:7b")
	if state != backend.ModelStateWarm {
		t.Fatalf("final state = %s, want WARM", state)
	}
	if ollama.loadCalls.Load() != 1 {
		t.Fatalf("LoadModel calls = %d, want 1", ollama.loadCalls.Load())
	}
}

// T3.4.2 — WARM model stays WARM on subsequent requests
func TestModelLifecycle_WarmStaysWarm(t *testing.T) {
	reg := backend.NewRegistry()
	ollama := &mockBackend{name: "ollama", typ: backend.BackendTypeLocal, healthy: true}
	_ = reg.Register(ollama)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	lc := orchestrator.NewModelLifecycle(reg, newStubVRAM(), 2*time.Second)
	ctx := context.Background()

	if err := lc.EnsureWarm(ctx, "codellama:7b"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	lc.TouchWarm("codellama:7b")
	if err := lc.EnsureWarm(ctx, "codellama:7b"); err != nil {
		t.Fatal(err)
	}

	if ollama.loadCalls.Load() != 1 {
		t.Fatalf("LoadModel calls = %d, want 1", ollama.loadCalls.Load())
	}
	state, _ := lc.State("codellama:7b")
	if state != backend.ModelStateWarm {
		t.Fatalf("state = %s, want WARM", state)
	}
}

// T3.4.3 — WARM → UNLOADING → COLD on idle timeout
func TestModelLifecycle_IdleUnload(t *testing.T) {
	reg := backend.NewRegistry()
	ollama := &mockBackend{name: "ollama", typ: backend.BackendTypeLocal, healthy: true}
	_ = reg.Register(ollama)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	vram := newStubVRAM()
	lc := orchestrator.NewModelLifecycle(reg, vram, time.Second)
	ctx := context.Background()

	if err := lc.EnsureWarm(ctx, "codellama:7b"); err != nil {
		t.Fatal(err)
	}
	if !vram.IsReserved("codellama:7b") {
		t.Fatal("VRAM not reserved after load")
	}

	time.Sleep(1500 * time.Millisecond)

	state, _ := lc.State("codellama:7b")
	if state != backend.ModelStateCold {
		t.Fatalf("state after idle = %s, want COLD", state)
	}
	if ollama.unloadCalls.Load() != 1 {
		t.Fatalf("UnloadModel calls = %d, want 1", ollama.unloadCalls.Load())
	}
	if vram.IsReserved("codellama:7b") {
		t.Fatal("VRAM not released after unload")
	}
}

// T3.4.4 — Request during LOADING state queues until ready
func TestModelLifecycle_LoadingQueuesRequests(t *testing.T) {
	reg := backend.NewRegistry()
	ollama := &mockBackend{
		name:      "ollama",
		typ:       backend.BackendTypeLocal,
		healthy:   true,
		loadDelay: 500 * time.Millisecond,
	}
	_ = reg.Register(ollama)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	lc := orchestrator.NewModelLifecycle(reg, newStubVRAM(), time.Minute)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)

	go func() {
		defer wg.Done()
		errs[0] = lc.EnsureWarm(ctx, "codellama:7b")
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		defer wg.Done()
		errs[1] = lc.EnsureWarm(ctx, "codellama:7b")
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if ollama.loadCalls.Load() != 1 {
		t.Fatalf("LoadModel calls = %d, want 1", ollama.loadCalls.Load())
	}
	state, _ := lc.State("codellama:7b")
	if state != backend.ModelStateWarm {
		t.Fatalf("state = %s, want WARM", state)
	}
}

// T3.4.5 — LOADING fails → falls back (does not get stuck)
func TestModelLifecycle_LoadFailureReturnsCold(t *testing.T) {
	reg := backend.NewRegistry()
	ollama := &mockBackend{
		name:    "ollama",
		typ:     backend.BackendTypeLocal,
		healthy: true,
		loadErr: errors.New("OOM"),
	}
	_ = reg.Register(ollama)
	registerModel(reg, "codellama:7b", "ollama", 4200)

	lc := orchestrator.NewModelLifecycle(reg, newStubVRAM(), time.Minute)
	err := lc.EnsureWarm(context.Background(), "codellama:7b")
	if err == nil {
		t.Fatal("expected load error")
	}
	state, _ := lc.State("codellama:7b")
	if state != backend.ModelStateCold {
		t.Fatalf("state = %s, want COLD after failure", state)
	}

	// Should not be stuck — second attempt can retry
	if err2 := lc.EnsureWarm(context.Background(), "codellama:7b"); err2 == nil {
		t.Fatal("expected second load to fail too")
	}
	if state, _ := lc.State("codellama:7b"); state != backend.ModelStateCold {
		t.Fatalf("state = %s, want COLD", state)
	}
}

func TestModelLifecycle_LoadFailureTriggersFallback(t *testing.T) {
	reg := backend.NewRegistry()
	local := &mockBackend{
		name:    "ollama",
		typ:     backend.BackendTypeLocal,
		healthy: true,
		loadErr: errors.New("OOM"),
	}
	cloud := &mockBackend{
		name:       "openai",
		typ:        backend.BackendTypeCloud,
		healthy:    true,
		completeFn: chunkStream("cloud response"),
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
	req := &backend.CompletionRequest{Model: "codellama:7b", Messages: []backend.Message{{Role: "user", Content: "hi"}}}

	ch, err := exec.Execute(context.Background(), decision, req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var b strings.Builder
	for c := range ch {
		b.WriteString(c.Content)
	}
	if b.String() != "cloud response" {
		t.Fatalf("content = %q, want cloud response", b.String())
	}
}
