package vram

import "time"

// AllocationStrategy controls how models are kept in VRAM.
type AllocationStrategy string

const (
	StrategyStatic  AllocationStrategy = "static"
	StrategyDynamic AllocationStrategy = "dynamic"
	StrategyHybrid  AllocationStrategy = "hybrid"
)

// GPUMemoryInfo holds GPU memory stats in bytes.
type GPUMemoryInfo struct {
	Total int64
	Used  int64
	Free  int64
}

// ModelAllocation tracks a model's VRAM reservation.
type ModelAllocation struct {
	Model    string
	Bytes    int64
	LastUsed time.Time
	KeepWarm bool
}

// EvictionPlan describes models to unload to make room.
type EvictionPlan struct {
	ModelsToEvict []string
	BytesFreed    int64
}

// VRAMState is a snapshot of GPU memory and loaded models.
type VRAMState struct {
	TotalBytes    int64
	UsedBytes     int64
	FreeBytes     int64
	HeadroomBytes int64
	LoadedModels  []ModelAllocation
	GPUIndex      int
}

// BatchReservation holds an atomic multi-model reservation.
type BatchReservation struct {
	Allocations []ModelAllocation
}
