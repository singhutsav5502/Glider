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
