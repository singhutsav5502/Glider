package swarm

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/glider-ai/glider/internal/config"
)

// ModuleKind classifies hot-swap vs restart-required modules.
type ModuleKind string

const (
	ModuleRouter   ModuleKind = "router"
	ModuleLogLevel ModuleKind = "log"
	ModuleGPU      ModuleKind = "gpu"
	ModuleBackend  ModuleKind = "backend"
	ModuleMITM     ModuleKind = "mitm"
	ModulePorts    ModuleKind = "ports"
	ModuleFanOut   ModuleKind = "fan_out"
)

// Module is a named swappable unit behind config.Provider Watch/Swap.
type Module struct {
	Name string
	Kind ModuleKind
	// Hot is true when Apply may run without process restart.
	Hot bool
	// Apply receives the new config snapshot. Must be idempotent and not mutate
	// in-flight RoutingDecision values (snapshot-at-Route contract).
	Apply func(cfg *config.Config) error

	gen atomic.Uint64
}

// Generation returns how many successful Applies have run.
func (m *Module) Generation() uint64 {
	if m == nil {
		return 0
	}
	return m.gen.Load()
}

// Registry tracks modules and binds them to config.Provider.Watch.
//
// Hot today (via Provider.Swap / file reload): router rules, aliases, context
// threshold, slog level, GPU assignments.
// Restart required: listen ports, MITM enable/port/CA/hosts, backend URLs/clients,
// cloud provider registration.
//
// See planning/context_and_swarm_architecture.md §3.
type Registry struct {
	mu       sync.Mutex
	modules  map[string]*Module
	provider *config.Provider
	bound    bool
}

// NewRegistry creates an empty hot-swap registry.
func NewRegistry() *Registry {
	return &Registry{modules: make(map[string]*Module)}
}

// Register adds or replaces a module by name.
func (r *Registry) Register(m *Module) error {
	if r == nil || m == nil || m.Name == "" {
		return fmt.Errorf("swarm: invalid module")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[m.Name] = m
	return nil
}

// BindProvider subscribes to Watch once and Apply()s Hot modules on each Swap/reload.
func (r *Registry) BindProvider(p *config.Provider) {
	if r == nil || p == nil {
		return
	}
	r.mu.Lock()
	if r.bound {
		r.mu.Unlock()
		return
	}
	r.provider = p
	r.bound = true
	r.mu.Unlock()
	p.Watch(func(cfg *config.Config) {
		r.applyAll(cfg)
	})
}

func (r *Registry) applyAll(cfg *config.Config) {
	r.mu.Lock()
	mods := make([]*Module, 0, len(r.modules))
	for _, m := range r.modules {
		mods = append(mods, m)
	}
	r.mu.Unlock()
	for _, m := range mods {
		if m == nil || !m.Hot || m.Apply == nil {
			continue
		}
		if err := m.Apply(cfg); err == nil {
			m.gen.Add(1)
		}
	}
}

// Docs returns a stable operator-facing map of module → hot|restart.
func Docs() map[string]string {
	return map[string]string{
		"router rules / aliases / threshold / log": "hot — config.Provider.Watch/Swap",
		"vram.gpu_assignments":                     "hot — same Swap path",
		"orchestration.fan_out / concurrency":      "hot if FanOutExecutor re-reads config; else next process",
		"backend clients (Ollama/vLLM/OpenAI)":     "restart — drain in-flight Complete first (future)",
		"MITM CA / hosts / ports":                  "restart — CONNECT listeners process-bound",
		"server listen ports":                      "restart",
	}
}

// List returns registered module names and whether they are hot.
func (r *Registry) List() []Module {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Module, 0, len(r.modules))
	for _, m := range r.modules {
		cp := *m
		cp.Apply = nil
		out = append(out, cp)
	}
	return out
}
