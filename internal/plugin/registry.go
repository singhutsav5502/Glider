package plugin

import (
	"context"
	"fmt"
	"sync"
)

// MemRegistry is an in-process plugin Registry.
type MemRegistry struct {
	mu      sync.RWMutex
	host    Host
	plugins map[string]Plugin
	states  map[string]State
}

// NewMemRegistry creates a registry bound to host (may be nil → SimpleHost).
func NewMemRegistry(host Host) *MemRegistry {
	if host == nil {
		host = &SimpleHost{}
	}
	return &MemRegistry{
		host:    host,
		plugins: make(map[string]Plugin),
		states:  make(map[string]State),
	}
}

func (r *MemRegistry) Register(p Plugin) error {
	if r == nil || p == nil {
		return fmt.Errorf("nil registry or plugin")
	}
	id := p.Meta().ID
	if id == "" {
		return fmt.Errorf("plugin id required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.plugins[id]; ok {
		return fmt.Errorf("plugin %q already registered", id)
	}
	if err := p.Register(context.Background(), r.host); err != nil {
		return err
	}
	r.plugins[id] = p
	r.states[id] = StateRegistered
	return nil
}

func (r *MemRegistry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	return p, ok
}

func (r *MemRegistry) List() []Meta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Meta, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p.Meta())
	}
	return out
}

func (r *MemRegistry) InitAll(ctx context.Context, cfg map[string]map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.plugins {
		c := map[string]string{}
		if cfg != nil {
			c = cfg[id]
		}
		if err := p.Init(ctx, c); err != nil {
			r.states[id] = StateFailed
			return fmt.Errorf("init %s: %w", id, err)
		}
		r.states[id] = StateInitialized
	}
	return nil
}

func (r *MemRegistry) StartAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.plugins {
		if err := p.Start(ctx); err != nil {
			r.states[id] = StateFailed
			return fmt.Errorf("start %s: %w", id, err)
		}
		r.states[id] = StateStarted
	}
	return nil
}

func (r *MemRegistry) StopAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for id, p := range r.plugins {
		if err := p.Stop(ctx); err != nil && first == nil {
			first = err
			r.states[id] = StateFailed
			continue
		}
		r.states[id] = StateStopped
	}
	return first
}

func (r *MemRegistry) HealthAll(ctx context.Context) map[string]Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Health, len(r.plugins))
	for id, p := range r.plugins {
		h := p.Health(ctx)
		if h.State == "" {
			h.State = r.states[id]
		}
		out[id] = h
	}
	return out
}

func (r *MemRegistry) ToolProviders() []ToolProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolProvider
	for _, p := range r.plugins {
		if tp := p.Tools(); tp != nil {
			out = append(out, tp)
		}
	}
	return out
}

// Base embeds no-op lifecycle helpers for simple plugins.
type Base struct {
	M    Meta
	Caps []Capability
	Host Host
	St   State
}

func (b *Base) Meta() Meta                     { return b.M }
func (b *Base) Capabilities() []Capability     { return b.Caps }
func (b *Base) Register(_ context.Context, h Host) error {
	b.Host = h
	b.St = StateRegistered
	return nil
}
func (b *Base) Init(context.Context, map[string]string) error {
	b.St = StateInitialized
	return nil
}
func (b *Base) Start(context.Context) error {
	b.St = StateStarted
	return nil
}
func (b *Base) Stop(context.Context) error {
	b.St = StateStopped
	return nil
}
func (b *Base) Health(context.Context) Health {
	return Health{OK: b.St != StateFailed, State: b.St}
}
func (b *Base) Tools() ToolProvider { return nil }
