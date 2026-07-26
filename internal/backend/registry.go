package backend

import (
	"fmt"
	"sync"
)

// Registry holds backends and model metadata.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]InferenceBackend
	models   map[string]*ModelInfo
}

func NewRegistry() *Registry {
	return &Registry{
		backends: make(map[string]InferenceBackend),
		models:   make(map[string]*ModelInfo),
	}
}

func (r *Registry) Register(b InferenceBackend) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := b.Name()
	if _, exists := r.backends[name]; exists {
		return fmt.Errorf("backend %q already registered", name)
	}
	r.backends[name] = b
	return nil
}

func (r *Registry) Get(name string) (InferenceBackend, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[name]
	if !ok {
		return nil, fmt.Errorf("backend %q not found", name)
	}
	return b, nil
}

func (r *Registry) List() []InferenceBackend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]InferenceBackend, 0, len(r.backends))
	for _, b := range r.backends {
		out = append(out, b)
	}
	return out
}

func (r *Registry) RegisterModel(info ModelInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info.State == "" {
		info.State = ModelStateCold
	}
	cp := info
	r.models[info.Name] = &cp
	return nil
}

func (r *Registry) GetModel(name string) (*ModelInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[name]
	if !ok {
		return nil, fmt.Errorf("model %q not found", name)
	}
	cp := *m
	return &cp, nil
}

func (r *Registry) SetModelState(name string, state ModelState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.models[name]
	if !ok {
		return fmt.Errorf("model %q not found", name)
	}
	m.State = state
	return nil
}

func (r *Registry) FindByCapability(cap string) []*ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*ModelInfo
	for _, m := range r.models {
		for _, c := range m.Capabilities {
			if c == cap {
				cp := *m
				out = append(out, &cp)
				break
			}
		}
	}
	return out
}

func (r *Registry) ListModels() []*ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ModelInfo, 0, len(r.models))
	for _, m := range r.models {
		cp := *m
		out = append(out, &cp)
	}
	return out
}

// ReplaceAll atomically swaps backend clients and model metadata.
// Existing model State is preserved for names that remain in the new set.
// Callers must pass fully built maps; on failure they should not call this
// so in-flight Complete() holders keep the previous InferenceBackend instances.
func (r *Registry) ReplaceAll(backends map[string]InferenceBackend, models []ModelInfo) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	oldStates := make(map[string]ModelState, len(r.models))
	for name, m := range r.models {
		if m != nil {
			oldStates[name] = m.State
		}
	}

	nextBackends := make(map[string]InferenceBackend, len(backends))
	for k, v := range backends {
		if v == nil {
			continue
		}
		nextBackends[k] = v
	}
	nextModels := make(map[string]*ModelInfo, len(models))
	for _, info := range models {
		if info.Name == "" {
			continue
		}
		cp := info
		if cp.State == "" {
			if s, ok := oldStates[cp.Name]; ok {
				cp.State = s
			} else {
				cp.State = ModelStateCold
			}
		}
		nextModels[cp.Name] = &cp
	}
	r.backends = nextBackends
	r.models = nextModels
}
