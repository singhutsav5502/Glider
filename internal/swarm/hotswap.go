package swarm

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/glider-ai/glider/internal/config"
)

// ModuleKind classifies hot-swap units. Loop Engineering stages are first-class:
// planner / actor / critic / memory / router. Infra kinds remain for process restart.
// See planning/loop_engineering.md.
type ModuleKind string

const (
	// Loop Engineering stages (hot-swappable prompts / enable flags).
	ModulePlanner ModuleKind = "planner"
	ModuleActor   ModuleKind = "actor"
	ModuleCritic  ModuleKind = "critic"
	ModuleMemory  ModuleKind = "memory"
	ModuleRouter  ModuleKind = "router"

	// Infra / orchestration (existing).
	ModuleClassifier    ModuleKind = "classifier"
	ModuleLogLevel      ModuleKind = "log"
	ModuleGPU           ModuleKind = "gpu"
	ModuleBackend       ModuleKind = "backend"
	ModuleMITM          ModuleKind = "mitm"
	ModulePorts         ModuleKind = "ports"
	ModuleFanOut        ModuleKind = "fan_out"
	ModuleSwarm         ModuleKind = "swarm"
	ModuleLoop          ModuleKind = "loop"
	ModuleSwarmTemplate ModuleKind = "swarm_template"
)

// Module is a named swappable unit behind config.Provider Watch/Swap.
type Module struct {
	Name        string     `json:"name"`
	Kind        ModuleKind `json:"kind"`
	Hot         bool       `json:"hot"`
	Enabled     bool       `json:"enabled"`
	Stage       bool       `json:"stage,omitempty"` // true when this is a Loop Engineering stage
	Description string     `json:"description,omitempty"`
	// Apply receives the new config snapshot. Must be idempotent and not mutate
	// in-flight RoutingDecision values (snapshot-at-Route contract).
	Apply func(cfg *config.Config) error `json:"-"`

	gen     atomic.Uint64
	enabled atomic.Bool
}

// Generation returns how many successful Applies have run.
func (m *Module) Generation() uint64 {
	if m == nil {
		return 0
	}
	return m.gen.Load()
}

// IsEnabled reports whether Apply is allowed to run for this module.
func (m *Module) IsEnabled() bool {
	if m == nil {
		return false
	}
	return m.enabled.Load()
}

// Registry tracks modules and binds them to config.Provider.Watch.
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

// Register adds or replaces a module by name. Starts enabled.
func (r *Registry) Register(m *Module) error {
	if r == nil || m == nil || m.Name == "" {
		return fmt.Errorf("swarm: invalid module")
	}
	m.enabled.Store(true)
	m.Enabled = true
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[m.Name] = m
	return nil
}

// SetEnabled toggles whether a hot module participates in Apply on Swap.
func (r *Registry) SetEnabled(name string, enabled bool) error {
	if r == nil {
		return fmt.Errorf("swarm: nil registry")
	}
	r.mu.Lock()
	m, ok := r.modules[name]
	r.mu.Unlock()
	if !ok || m == nil {
		return fmt.Errorf("swarm: unknown module %q", name)
	}
	m.enabled.Store(enabled)
	m.Enabled = enabled
	return nil
}

// BindProvider subscribes to Watch once and Apply()s Hot+enabled modules on each Swap/reload.
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
		if m == nil || !m.Hot || m.Apply == nil || !m.enabled.Load() {
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
		"planner / actor / critic / memory / router": "hot — Loop Engineering stages (enable/disable)",
		"router rules / aliases / threshold / log":   "hot — config.Provider.Watch/Swap",
		"vram.gpu_assignments":                       "hot — same Swap path",
		"orchestration.fan_out / swarm / concurrency": "hot — FanOutExecutor + Runner Apply",
		"loop hoop specs + eval":                     "hot — loop.Manager reload",
		"swarm templates (~/.glider/hoops)":          "hot — TemplateStore file reload",
		"backend clients (Ollama/vLLM/OpenAI)":       "restart — drain in-flight Complete first",
		"MITM CA / hosts / ports":                    "restart — CONNECT listeners process-bound",
		"server listen ports":                        "restart",
	}
}

// ModuleInfo is a listable snapshot for the dashboard.
type ModuleInfo struct {
	Name        string     `json:"name"`
	Kind        ModuleKind `json:"kind"`
	Hot         bool       `json:"hot"`
	Enabled     bool       `json:"enabled"`
	Stage       bool       `json:"stage,omitempty"`
	Description string     `json:"description,omitempty"`
	Generation  uint64     `json:"generation"`
	Reload      string     `json:"reload"` // hot | restart
}

// List returns registered modules for the dashboard.
func (r *Registry) List() []ModuleInfo {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ModuleInfo, 0, len(r.modules))
	for _, m := range r.modules {
		reload := "restart"
		if m.Hot {
			reload = "hot"
		}
		out = append(out, ModuleInfo{
			Name:        m.Name,
			Kind:        m.Kind,
			Hot:         m.Hot,
			Enabled:     m.enabled.Load(),
			Stage:       m.Stage,
			Description: m.Description,
			Generation:  m.gen.Load(),
			Reload:      reload,
		})
	}
	return out
}

// BuiltinCatalog returns Loop Engineering stages + infra matrix.
func BuiltinCatalog() []ModuleInfo {
	return []ModuleInfo{
		{Name: "router", Kind: ModuleRouter, Hot: true, Enabled: true, Stage: true, Reload: "hot", Description: "Choose local vs cloud / model"},
		{Name: "planner", Kind: ModulePlanner, Hot: true, Enabled: true, Stage: true, Reload: "hot", Description: "Decompose goal → next action"},
		{Name: "actor", Kind: ModuleActor, Hot: true, Enabled: true, Stage: true, Reload: "hot", Description: "Maker — execute one bounded action"},
		{Name: "critic", Kind: ModuleCritic, Hot: true, Enabled: true, Stage: true, Reload: "hot", Description: "Checker — grade actor vs eval goal"},
		{Name: "memory", Kind: ModuleMemory, Hot: true, Enabled: true, Stage: true, Reload: "hot", Description: "Durable episode / STATE outside the turn"},
		{Name: "fan_out", Kind: ModuleFanOut, Hot: true, Enabled: true, Reload: "hot", Description: "Gateway multi-worker fan-out"},
		{Name: "swarm", Kind: ModuleSwarm, Hot: true, Enabled: true, Reload: "hot", Description: "POST /api/swarm/run runner"},
		{Name: "loop", Kind: ModuleLoop, Hot: true, Enabled: true, Reload: "hot", Description: "Hoop manager + learning"},
		{Name: "backends", Kind: ModuleBackend, Hot: false, Enabled: true, Reload: "restart"},
		{Name: "mitm", Kind: ModuleMITM, Hot: false, Enabled: true, Reload: "restart"},
		{Name: "ports", Kind: ModulePorts, Hot: false, Enabled: true, Reload: "restart"},
	}
}

// RegisterLoopStages registers the five Loop Engineering stage modules (no-op Apply by default).
func (r *Registry) RegisterLoopStages() {
	if r == nil {
		return
	}
	for _, info := range BuiltinCatalog() {
		if !info.Stage {
			continue
		}
		info := info
		_ = r.Register(&Module{
			Name:        info.Name,
			Kind:        info.Kind,
			Hot:         true,
			Stage:       true,
			Description: info.Description,
			Apply:       func(cfg *config.Config) error { return nil },
		})
	}
}
