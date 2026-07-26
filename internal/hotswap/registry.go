// Package hotswap tracks named config-reload units bound to config.Provider's
// Watch/Swap, distinct from the routing/rulesets/analytics they configure.
package hotswap

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/config"
)

// ModuleKind classifies hot-swap units.
type ModuleKind string

const (
	ModuleRouter     ModuleKind = "router"
	ModuleClassifier ModuleKind = "classifier"
	ModuleLogLevel   ModuleKind = "log"
	ModuleGPU        ModuleKind = "gpu"
	ModuleBackend    ModuleKind = "backend"
	ModuleMITM       ModuleKind = "mitm"
	ModulePorts      ModuleKind = "ports"
	ModuleFanOut     ModuleKind = "fan_out"
)

// Module is a named swappable unit behind config.Provider Watch/Swap.
type Module struct {
	Name        string     `json:"name"`
	Kind        ModuleKind `json:"kind"`
	Hot         bool       `json:"hot"`
	Enabled     bool       `json:"enabled"`
	Description string     `json:"description,omitempty"`
	// Apply receives the new config snapshot. Must be idempotent and not mutate
	// in-flight RoutingDecision values (snapshot-at-Route contract).
	Apply func(cfg *config.Config) error `json:"-"`
	// Status, when set, overrides recorded Apply result in List() (e.g. Reloader.Status).
	Status func() (ok bool, errMsg string, warnings []string, at time.Time, attempted bool) `json:"-"`

	gen     atomic.Uint64
	enabled atomic.Bool

	statusMu  sync.Mutex
	lastOK    bool
	lastErr   string
	lastAt    time.Time
	lastWarns []string
	attempted bool
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
		return fmt.Errorf("hotswap: invalid module")
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
		return fmt.Errorf("hotswap: nil registry")
	}
	r.mu.Lock()
	m, ok := r.modules[name]
	r.mu.Unlock()
	if !ok || m == nil {
		return fmt.Errorf("hotswap: unknown module %q", name)
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
		err := m.Apply(cfg)
		m.recordApply(err, nil)
		if err == nil {
			m.gen.Add(1)
		}
	}
}

func (m *Module) recordApply(err error, warnings []string) {
	if m == nil {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	m.attempted = true
	m.lastAt = time.Now().UTC()
	m.lastWarns = append([]string{}, warnings...)
	if err != nil {
		m.lastOK = false
		m.lastErr = err.Error()
		return
	}
	m.lastOK = true
	m.lastErr = ""
}

// RecordApplyStatus lets an Apply implementation attach warnings after success
// (e.g. backend warm-ping soft failures) without failing the swap.
func (m *Module) RecordApplyStatus(ok bool, errMsg string, warnings []string) {
	if m == nil {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	m.attempted = true
	m.lastAt = time.Now().UTC()
	m.lastOK = ok
	m.lastErr = errMsg
	m.lastWarns = append([]string{}, warnings...)
}

// ModuleByName returns a registered module (tests / status wiring).
func (r *Registry) ModuleByName(name string) *Module {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.modules[name]
}

// Docs returns a stable operator-facing map of module → hot|restart.
func Docs() map[string]string {
	return map[string]string{
		"router rules / aliases / threshold / log": "hot — config.Provider.Watch/Swap",
		"vram.gpu_assignments":                      "hot — same Swap path",
		"orchestration.fan_out / concurrency":       "hot — FanOutExecutor Apply",
		"backend clients (Ollama/vLLM/cloud)":       "hot — Registry.ReplaceAll; in-flight Complete keeps old client until cycle ends",
		"MITM CA / hosts / ports":                   "restart — CONNECT listeners process-bound",
		"server listen ports":                       "restart",
	}
}

// ModuleInfo is a listable snapshot for the dashboard.
type ModuleInfo struct {
	Name        string     `json:"name"`
	Kind        ModuleKind `json:"kind"`
	Hot         bool       `json:"hot"`
	Enabled     bool       `json:"enabled"`
	Description string     `json:"description,omitempty"`
	Generation  uint64     `json:"generation"`
	Reload      string     `json:"reload"` // hot | restart
	// Last reload signal (backends and other Apply modules).
	LastOK       *bool     `json:"last_ok,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	LastAt       time.Time `json:"last_at,omitempty"`
	LastWarnings []string  `json:"last_warnings,omitempty"`
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
		info := ModuleInfo{
			Name:        m.Name,
			Kind:        m.Kind,
			Hot:         m.Hot,
			Enabled:     m.enabled.Load(),
			Description: m.Description,
			Generation:  m.gen.Load(),
			Reload:      reload,
		}
		if m.Status != nil {
			ok, errMsg, warns, at, attempted := m.Status()
			if attempted {
				info.LastOK = &ok
				info.LastError = errMsg
				info.LastAt = at
				if len(warns) > 0 {
					info.LastWarnings = append([]string{}, warns...)
				}
			}
		} else {
			m.statusMu.Lock()
			if m.attempted {
				ok := m.lastOK
				info.LastOK = &ok
				info.LastError = m.lastErr
				info.LastAt = m.lastAt
				if len(m.lastWarns) > 0 {
					info.LastWarnings = append([]string{}, m.lastWarns...)
				}
			}
			m.statusMu.Unlock()
		}
		out = append(out, info)
	}
	return out
}

// BuiltinCatalog returns the infra hot-swap matrix.
func BuiltinCatalog() []ModuleInfo {
	return []ModuleInfo{
		{Name: "router", Kind: ModuleRouter, Hot: true, Enabled: true, Reload: "hot", Description: "Choose local vs cloud / model"},
		{Name: "classifier", Kind: ModuleClassifier, Hot: true, Enabled: true, Reload: "hot", Description: "Task classifier + role hints"},
		{Name: "fan_out", Kind: ModuleFanOut, Hot: true, Enabled: true, Reload: "hot", Description: "Gateway multi-worker fan-out"},
		{Name: "backends", Kind: ModuleBackend, Hot: true, Enabled: true, Reload: "hot", Description: "Ollama/vLLM/cloud clients + models (in-flight keeps old client)"},
		{Name: "mitm", Kind: ModuleMITM, Hot: false, Enabled: true, Reload: "restart"},
		{Name: "ports", Kind: ModulePorts, Hot: false, Enabled: true, Reload: "restart"},
	}
}
