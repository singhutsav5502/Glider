package vram

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ManagerConfig seeds initial GPU state for the allocator.
type ManagerConfig struct {
	TotalBytes    int64
	UsedBytes     int64
	FreeBytes     int64
	HeadroomBytes int64
	Strategy      AllocationStrategy
	GPUIndex      int
	// Unmetered says that no code could read the true size of the device.
	// The manager then keeps its record of what is loaded, and it permits
	// every reservation.
	//
	// This exists so that the caller never has to invent a number. Before
	// 2026-08-13 the caller passed a fixed 16 GiB. On a machine with a 4 GiB
	// card, every Reserve then succeeded, the model went to Ollama, and
	// Ollama reported a CUDA out-of-memory error. The check said yes to a
	// thing that could not happen. A fixed number that is too SMALL is not
	// better: it refuses work that the machine can do.
	//
	// "I do not know" is the third answer, and it is the honest one when
	// nvidia-smi is absent, or the GPU belongs to another supplier, or the
	// machine has no GPU.
	Unmetered bool
}

// Manager tracks VRAM reservations and eviction planning.
type Manager struct {
	mu sync.RWMutex

	totalBytes    int64
	usedBytes     int64
	freeBytes     int64
	headroomBytes int64
	strategy      AllocationStrategy
	gpuIndex      int
	unmetered     bool

	loaded []ModelAllocation
}

// Unmetered reports whether this manager has a true device size. A caller
// that shows VRAM to a person uses it to say "not measured" in place of a
// number that means nothing.
func (m *Manager) Unmetered() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.unmetered
}

// NewManager creates a VRAM manager with the given initial state.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Strategy == "" {
		cfg.Strategy = StrategyDynamic
	}
	return &Manager{
		totalBytes:    cfg.TotalBytes,
		usedBytes:     cfg.UsedBytes,
		freeBytes:     cfg.FreeBytes,
		headroomBytes: cfg.HeadroomBytes,
		strategy:      cfg.Strategy,
		gpuIndex:      cfg.GPUIndex,
		unmetered:     cfg.Unmetered,
		loaded:        make([]ModelAllocation, 0),
	}
}

// SetLoadedModels replaces tracked loaded models (for tests and sync).
func (m *Manager) SetLoadedModels(models []ModelAllocation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = append([]ModelAllocation(nil), models...)
}

// GetState returns a snapshot of current VRAM state.
func (m *Manager) GetState() *VRAMState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() *VRAMState {
	models := append([]ModelAllocation(nil), m.loaded...)
	return &VRAMState{
		TotalBytes:    m.totalBytes,
		UsedBytes:     m.usedBytes,
		FreeBytes:     m.freeBytes,
		HeadroomBytes: m.headroomBytes,
		LoadedModels:  models,
		GPUIndex:      m.gpuIndex,
	}
}

// SetHeadroom configures reserved free VRAM.
func (m *Manager) SetHeadroom(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.headroomBytes = bytes
}

// SetStrategy sets the allocation strategy.
func (m *Manager) SetStrategy(strategy AllocationStrategy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strategy = strategy
}

// CanFit reports whether a model fits, optionally with an LRU eviction plan.
func (m *Manager) CanFit(model string, requiredBytes int64) (bool, *EvictionPlan) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if requiredBytes <= 0 {
		return true, nil
	}
	// No measurement, no opinion. Refer to ManagerConfig.Unmetered.
	if m.unmetered {
		return true, nil
	}

	usable := m.freeBytes - m.headroomBytes
	if usable >= requiredBytes {
		return true, nil
	}

	if m.totalBytes-m.headroomBytes < requiredBytes {
		return false, nil
	}

	plan := m.buildEvictionPlanLocked(model, requiredBytes)
	if len(plan.ModelsToEvict) > 0 && m.freeBytes+plan.BytesFreed >= requiredBytes {
		return true, plan
	}

	if len(plan.ModelsToEvict) == 0 {
		return false, &EvictionPlan{}
	}
	return false, plan
}

func (m *Manager) buildEvictionPlanLocked(excludeModel string, requiredBytes int64) *EvictionPlan {
	candidates := make([]ModelAllocation, 0, len(m.loaded))
	for _, model := range m.loaded {
		if model.Model == excludeModel {
			continue
		}
		if !m.canEvict(model) {
			continue
		}
		candidates = append(candidates, model)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastUsed.Before(candidates[j].LastUsed)
	})

	plan := &EvictionPlan{
		ModelsToEvict: make([]string, 0),
	}
	var freed int64
	for _, model := range candidates {
		if m.freeBytes+freed >= requiredBytes {
			break
		}
		plan.ModelsToEvict = append(plan.ModelsToEvict, model.Model)
		freed += model.Bytes
	}
	plan.BytesFreed = freed
	return plan
}

func (m *Manager) canEvict(model ModelAllocation) bool {
	switch m.strategy {
	case StrategyStatic:
		return !model.KeepWarm
	case StrategyHybrid:
		return !model.KeepWarm
	default:
		return true
	}
}

// Reserve records a VRAM allocation for a model.
func (m *Manager) Reserve(model string, bytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if bytes <= 0 {
		return fmt.Errorf("reserve bytes must be positive")
	}

	for _, loaded := range m.loaded {
		if loaded.Model == model {
			return fmt.Errorf("model %q already reserved", model)
		}
	}

	// An unmetered manager still keeps the record below — the dashboard and
	// the eviction order both need it — and only skips the capacity test.
	if !m.unmetered && m.freeBytes-m.headroomBytes < bytes {
		plan := m.buildEvictionPlanLocked(model, bytes)
		if plan == nil || m.freeBytes+plan.BytesFreed < bytes {
			return fmt.Errorf("insufficient VRAM for model %q", model)
		}
	}

	m.loaded = append(m.loaded, ModelAllocation{
		Model:    model,
		Bytes:    bytes,
		LastUsed: time.Now(),
	})
	m.usedBytes += bytes
	m.freeBytes -= bytes
	return nil
}

// Release removes a tracked VRAM allocation.
func (m *Manager) Release(model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	var bytes int64
	for i, loaded := range m.loaded {
		if loaded.Model == model {
			idx = i
			bytes = loaded.Bytes
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("model %q not reserved", model)
	}

	m.loaded = append(m.loaded[:idx], m.loaded[idx+1:]...)
	m.usedBytes -= bytes
	m.freeBytes += bytes
	return nil
}

// BatchReserve atomically reserves multiple models (all or nothing).
func (m *Manager) BatchReserve(models []ModelAllocation) (*BatchReservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(models) == 0 {
		return &BatchReservation{}, nil
	}

	var totalRequired int64
	names := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model.Model == "" {
			return nil, errors.New("batch model name required")
		}
		if model.Bytes <= 0 {
			return nil, fmt.Errorf("batch model %q bytes must be positive", model.Model)
		}
		if _, ok := names[model.Model]; ok {
			return nil, fmt.Errorf("duplicate model %q in batch", model.Model)
		}
		names[model.Model] = struct{}{}
		for _, loaded := range m.loaded {
			if loaded.Model == model.Model {
				return nil, fmt.Errorf("model %q already reserved", model.Model)
			}
		}
		totalRequired += model.Bytes
	}

	if !m.unmetered && m.freeBytes-m.headroomBytes < totalRequired {
		plan := m.buildEvictionPlanLocked("", totalRequired)
		if plan == nil || m.freeBytes+plan.BytesFreed < totalRequired {
			return nil, fmt.Errorf("insufficient VRAM for batch reservation")
		}
	}

	now := time.Now()
	res := &BatchReservation{Allocations: make([]ModelAllocation, 0, len(models))}
	for _, model := range models {
		allocation := ModelAllocation{
			Model:    model.Model,
			Bytes:    model.Bytes,
			LastUsed: now,
			KeepWarm: model.KeepWarm,
		}
		m.loaded = append(m.loaded, allocation)
		res.Allocations = append(res.Allocations, allocation)
	}
	m.usedBytes += totalRequired
	m.freeBytes -= totalRequired
	return res, nil
}

// BatchRelease releases all allocations in a batch reservation.
func (m *Manager) BatchRelease(reservation *BatchReservation) error {
	if reservation == nil {
		return nil
	}
	for _, model := range reservation.Allocations {
		if err := m.Release(model.Model); err != nil {
			return err
		}
	}
	return nil
}
