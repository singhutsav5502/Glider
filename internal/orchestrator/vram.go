package orchestrator

// VRAMManager tracks GPU memory for model lifecycle decisions.
// Narrow local interface matching the planned internal/vram package.
type VRAMManager interface {
	GetState() *VRAMState
	CanFit(model string, requiredBytes int64) (bool, *EvictionPlan)
	Reserve(model string, bytes int64) error
	Release(model string) error
}

// VRAMState is a snapshot of GPU memory.
type VRAMState struct {
	TotalBytes    int64
	UsedBytes     int64
	FreeBytes     int64
	HeadroomBytes int64
}

// EvictionPlan describes models that must be evicted to free space.
type EvictionPlan struct {
	ModelsToEvict []string
	BytesFreed    int64
}
