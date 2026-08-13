package orchestrator

import "github.com/glider-ai/glider/internal/vram"

// AdaptVRAM wraps internal/vram.Manager to satisfy orchestrator.VRAMManager.
type AdaptVRAM struct {
	Inner *vram.Manager
}

func (a AdaptVRAM) GetState() *VRAMState {
	st := a.Inner.GetState()
	if st == nil {
		return &VRAMState{}
	}
	return &VRAMState{
		TotalBytes:    st.TotalBytes,
		UsedBytes:     st.UsedBytes,
		FreeBytes:     st.FreeBytes,
		HeadroomBytes: st.HeadroomBytes,
	}
}

func (a AdaptVRAM) CanFit(model string, requiredBytes int64) (bool, *EvictionPlan) {
	ok, plan := a.Inner.CanFit(model, requiredBytes)
	if plan == nil {
		return ok, nil
	}
	return ok, &EvictionPlan{ModelsToEvict: plan.ModelsToEvict, BytesFreed: plan.BytesFreed}
}

func (a AdaptVRAM) Reserve(model string, bytes int64) error {
	return a.Inner.Reserve(model, bytes)
}

func (a AdaptVRAM) Release(model string) error {
	return a.Inner.Release(model)
}
