package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

type mockBackend struct {
	name       string
	typ        backend.BackendType
	healthy    bool
	loadDelay  time.Duration
	loadErr    error
	unloadErr  error
	completeFn func(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)

	loadCalls   atomic.Int32
	unloadCalls atomic.Int32
	mu          sync.Mutex
}

func (m *mockBackend) Name() string              { return m.name }
func (m *mockBackend) Type() backend.BackendType { return m.typ }

func (m *mockBackend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, req)
	}
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: "ok", Model: req.Model}
	close(ch)
	return ch, nil
}

func (m *mockBackend) LoadModel(ctx context.Context, model string, opts backend.LoadOptions) error {
	m.loadCalls.Add(1)
	if m.loadDelay > 0 {
		select {
		case <-time.After(m.loadDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.loadErr
}

func (m *mockBackend) UnloadModel(ctx context.Context, model string) error {
	m.unloadCalls.Add(1)
	return m.unloadErr
}

func (m *mockBackend) ListLoaded(ctx context.Context) ([]backend.LoadedModel, error) {
	return nil, nil
}

func (m *mockBackend) Ping(ctx context.Context) error {
	if m.healthy {
		return nil
	}
	return errors.New("unhealthy")
}

func (m *mockBackend) IsHealthy() bool { return m.healthy }

func chunkStream(parts ...string) func(context.Context, *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return func(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		ch := make(chan backend.CompletionChunk, len(parts))
		for _, p := range parts {
			ch <- backend.CompletionChunk{Content: p, Model: req.Model}
		}
		close(ch)
		return ch, nil
	}
}

func errorComplete(err error) func(context.Context, *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return func(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		return nil, err
	}
}

type stubVRAM struct {
	reserved map[string]int64
}

func newStubVRAM() *stubVRAM {
	return &stubVRAM{reserved: make(map[string]int64)}
}

func (s *stubVRAM) GetState() *orchestrator.VRAMState { return &orchestrator.VRAMState{} }
func (s *stubVRAM) CanFit(model string, requiredBytes int64) (bool, *orchestrator.EvictionPlan) {
	return true, nil
}
func (s *stubVRAM) Reserve(model string, bytes int64) error {
	s.reserved[model] = bytes
	return nil
}
func (s *stubVRAM) Release(model string) error {
	delete(s.reserved, model)
	return nil
}

func registerModel(reg *backend.Registry, name, backendName string, vramMB int) {
	_ = reg.RegisterModel(backend.ModelInfo{
		Name:           name,
		Backend:        backendName,
		VRAMEstimateMB: vramMB,
		State:          backend.ModelStateCold,
	})
}
