package vram

// VRAMManager tracks GPU memory and allocation policy.
type VRAMManager interface {
	GetState() *VRAMState
	CanFit(model string, requiredBytes int64) (bool, *EvictionPlan)
	Reserve(model string, bytes int64) error
	Release(model string) error
	BatchReserve(models []ModelAllocation) (*BatchReservation, error)
	BatchRelease(reservation *BatchReservation) error
	SetStrategy(strategy AllocationStrategy)
	SetHeadroom(bytes int64)
}

var _ VRAMManager = (*Manager)(nil)

// ParseStrategy converts a config string to AllocationStrategy.
func ParseStrategy(s string) AllocationStrategy {
	switch AllocationStrategy(s) {
	case StrategyStatic, StrategyDynamic, StrategyHybrid:
		return AllocationStrategy(s)
	default:
		return StrategyDynamic
	}
}
