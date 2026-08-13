package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

type loadFlight struct {
	wg  sync.WaitGroup
	err error
}

// ModelLifecycle manages COLD/LOADING/WARM/UNLOADING transitions.
type ModelLifecycle struct {
	registry    *backend.Registry
	vram        VRAMManager
	idleTimeout time.Duration

	mu         sync.Mutex
	inFlight   map[string]*loadFlight
	idleTimers map[string]*time.Timer
}

// NewModelLifecycle creates a lifecycle manager.
func NewModelLifecycle(registry *backend.Registry, vram VRAMManager, idleTimeout time.Duration) *ModelLifecycle {
	return &ModelLifecycle{
		registry:    registry,
		vram:        vram,
		idleTimeout: idleTimeout,
		inFlight:    make(map[string]*loadFlight),
		idleTimers:  make(map[string]*time.Timer),
	}
}

// EnsureWarm loads the model if needed and waits for LOADING to complete.
func (m *ModelLifecycle) EnsureWarm(ctx context.Context, modelName string) error {
	for {
		m.mu.Lock()
		info, err := m.registry.GetModel(modelName)
		if err != nil {
			m.mu.Unlock()
			return err
		}

		switch info.State {
		case backend.ModelStateWarm:
			m.resetIdleTimerLocked(modelName, info)
			m.mu.Unlock()
			return nil

		case backend.ModelStateLoading:
			flight := m.inFlight[modelName]
			m.mu.Unlock()
			if flight == nil {
				return nil
			}
			waitDone := make(chan struct{})
			go func() {
				flight.wg.Wait()
				close(waitDone)
			}()
			select {
			case <-waitDone:
				return flight.err
			case <-ctx.Done():
				return ctx.Err()
			}

		case backend.ModelStateCold:
			flight := &loadFlight{}
			flight.wg.Add(1)
			m.inFlight[modelName] = flight
			_ = m.registry.SetModelState(modelName, backend.ModelStateLoading)
			m.mu.Unlock()

			loadErr := m.loadModel(ctx, modelName, info)
			m.mu.Lock()
			if loadErr != nil {
				_ = m.registry.SetModelState(modelName, backend.ModelStateCold)
			} else {
				_ = m.registry.SetModelState(modelName, backend.ModelStateWarm)
				m.resetIdleTimerLocked(modelName, info)
			}
			flight.err = loadErr
			flight.wg.Done()
			delete(m.inFlight, modelName)
			m.mu.Unlock()
			return loadErr

		default:
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
}

// TouchWarm resets the idle timer for a WARM model after a request.
func (m *ModelLifecycle) TouchWarm(modelName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := m.registry.GetModel(modelName)
	if err != nil || info.State != backend.ModelStateWarm {
		return
	}
	m.resetIdleTimerLocked(modelName, info)
}

func (m *ModelLifecycle) loadModel(ctx context.Context, modelName string, info *backend.ModelInfo) error {
	if m.vram != nil && info.VRAMEstimateMB > 0 {
		bytes := int64(info.VRAMEstimateMB) * 1024 * 1024
		if err := m.vram.Reserve(modelName, bytes); err != nil {
			return err
		}
	}

	b, err := m.registry.Get(info.Backend)
	if err != nil {
		if m.vram != nil && info.VRAMEstimateMB > 0 {
			_ = m.vram.Release(modelName)
		}
		return err
	}
	mm, ok := b.(backend.ModelManager)
	if !ok {
		return nil
	}
	if err := mm.LoadModel(ctx, modelName, backend.LoadOptions{}); err != nil {
		if m.vram != nil && info.VRAMEstimateMB > 0 {
			_ = m.vram.Release(modelName)
		}
		return err
	}
	return nil
}

func (m *ModelLifecycle) resetIdleTimerLocked(modelName string, info *backend.ModelInfo) {
	if t, ok := m.idleTimers[modelName]; ok {
		t.Stop()
	}
	if m.idleTimeout <= 0 || info.KeepWarm {
		return
	}
	m.idleTimers[modelName] = time.AfterFunc(m.idleTimeout, func() {
		m.unload(modelName)
	})
}

func (m *ModelLifecycle) unload(modelName string) {
	info, err := m.registry.GetModel(modelName)
	if err != nil {
		return
	}
	_ = m.registry.SetModelState(modelName, backend.ModelStateUnloading)

	if b, err := m.registry.Get(info.Backend); err == nil {
		if mm, ok := b.(backend.ModelManager); ok {
			_ = mm.UnloadModel(context.Background(), modelName)
		}
	}
	if m.vram != nil && info.VRAMEstimateMB > 0 {
		_ = m.vram.Release(modelName)
	}
	_ = m.registry.SetModelState(modelName, backend.ModelStateCold)

	m.mu.Lock()
	if t, ok := m.idleTimers[modelName]; ok {
		t.Stop()
		delete(m.idleTimers, modelName)
	}
	m.mu.Unlock()
}

// State returns the current model state from the registry.
func (m *ModelLifecycle) State(modelName string) (backend.ModelState, error) {
	info, err := m.registry.GetModel(modelName)
	if err != nil {
		return "", err
	}
	return info.State, nil
}
