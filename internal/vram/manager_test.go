package vram_test

import (
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/vram"
)

const (
	gb = 1024 * 1024 * 1024
	mb = 1024 * 1024
)

func newTestManager(total, used, free, headroom int64) *vram.Manager {
	m := vram.NewManager(vram.ManagerConfig{
		TotalBytes:    total,
		UsedBytes:     used,
		FreeBytes:     free,
		HeadroomBytes: headroom,
		Strategy:      vram.StrategyDynamic,
	})
	return m
}

// T3.3.1 — CanFit: model fits in free VRAM
func TestManager_CanFit_FitsWithoutEviction(t *testing.T) {
	m := newTestManager(8*gb, 2*gb, 6*gb, 512*mb)
	ok, plan := m.CanFit("model-x", 4*gb)
	if !ok || plan != nil {
		t.Fatalf("expected (true, nil), got (%v, %v)", ok, plan)
	}
}

// T3.3.2 — CanFit: model needs eviction
func TestManager_CanFit_NeedsEviction(t *testing.T) {
	now := time.Now()
	m := newTestManager(8*gb, 6*gb, 2*gb, 512*mb)
	m.SetLoadedModels([]vram.ModelAllocation{
		{Model: "A", Bytes: 2 * gb, LastUsed: now.Add(-10 * time.Minute)},
		{Model: "B", Bytes: 4 * gb, LastUsed: now.Add(-1 * time.Minute)},
	})

	ok, plan := m.CanFit("model-x", 4*gb)
	if !ok {
		t.Fatal("expected CanFit to return true with eviction plan")
	}
	if plan == nil {
		t.Fatal("expected eviction plan")
	}
	if len(plan.ModelsToEvict) != 1 || plan.ModelsToEvict[0] != "A" {
		t.Fatalf("expected to evict model A, got %v", plan.ModelsToEvict)
	}
	if plan.BytesFreed != 2*gb {
		t.Fatalf("BytesFreed: got %d want %d", plan.BytesFreed, 2*gb)
	}
}

// T3.3.3 — CanFit: model cannot fit even after full eviction
func TestManager_CanFit_CannotFit(t *testing.T) {
	m := newTestManager(8*gb, 0, 8*gb, 512*mb)
	ok, plan := m.CanFit("model-x", 10*gb)
	if ok || plan != nil {
		t.Fatalf("expected (false, nil), got (%v, %v)", ok, plan)
	}
}

// T3.3.4 — Headroom is respected
func TestManager_CanFit_HeadroomRespected(t *testing.T) {
	m := newTestManager(8*gb, 3*gb, 5*gb, 2*gb)
	ok, plan := m.CanFit("model-x", 4*gb)
	if ok {
		t.Fatal("expected CanFit to return false when headroom leaves insufficient space")
	}
	if plan == nil {
		t.Fatal("expected eviction plan when headroom blocks allocation")
	}
}

// T3.3.5 — Reserve and release VRAM
func TestManager_ReserveAndRelease(t *testing.T) {
	m := newTestManager(8*gb, 0, 8*gb, 0)
	if err := m.Reserve("model-a", 4*gb); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	state := m.GetState()
	if got := trackedBytes(state); got != 4*gb {
		t.Fatalf("after reserve: tracked %d want %d", got, 4*gb)
	}
	if err := m.Release("model-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	state = m.GetState()
	if got := trackedBytes(state); got != 0 {
		t.Fatalf("after release: tracked %d want 0", got)
	}
}

// T3.3.6 — BatchReserve allocates atomically
func TestManager_BatchReserve_AllOrNothing(t *testing.T) {
	m := newTestManager(8*gb, 2*gb, 6*gb, 0)
	models := []vram.ModelAllocation{
		{Model: "model-a", Bytes: 4 * gb},
		{Model: "model-b", Bytes: 4 * gb},
	}
	res, err := m.BatchReserve(models)
	if err == nil {
		t.Fatal("expected BatchReserve to fail when batch exceeds available VRAM")
	}
	if res != nil {
		t.Fatal("expected nil reservation on failure")
	}
	if got := trackedBytes(m.GetState()); got != 0 {
		t.Fatalf("state should be unchanged, tracked %d", got)
	}
}

func TestManager_BatchReserve_Success(t *testing.T) {
	m := newTestManager(8*gb, 0, 8*gb, 0)
	models := []vram.ModelAllocation{
		{Model: "model-a", Bytes: 2 * gb},
		{Model: "model-b", Bytes: 2 * gb},
	}
	res, err := m.BatchReserve(models)
	if err != nil {
		t.Fatalf("BatchReserve: %v", err)
	}
	if res == nil || len(res.Allocations) != 2 {
		t.Fatalf("expected reservation with 2 models, got %v", res)
	}
	if got := trackedBytes(m.GetState()); got != 4*gb {
		t.Fatalf("tracked %d want %d", got, 4*gb)
	}
	if err := m.BatchRelease(res); err != nil {
		t.Fatalf("BatchRelease: %v", err)
	}
	if got := trackedBytes(m.GetState()); got != 0 {
		t.Fatalf("after batch release: tracked %d want 0", got)
	}
}

func TestManager_ReserveDuplicate(t *testing.T) {
	m := newTestManager(8*gb, 0, 8*gb, 0)
	if err := m.Reserve("model-a", 2*gb); err != nil {
		t.Fatal(err)
	}
	if err := m.Reserve("model-a", 1*gb); err == nil {
		t.Fatal("expected error for duplicate reserve")
	}
}

func TestParseStrategy(t *testing.T) {
	if got := vram.ParseStrategy("static"); got != vram.StrategyStatic {
		t.Fatalf("got %q", got)
	}
	if got := vram.ParseStrategy("unknown"); got != vram.StrategyDynamic {
		t.Fatalf("default strategy: got %q", got)
	}
}

func TestManager_SetHeadroom(t *testing.T) {
	m := newTestManager(8*gb, 0, 8*gb, 512*mb)
	m.SetHeadroom(1 * gb)
	if got := m.GetState().HeadroomBytes; got != 1*gb {
		t.Fatalf("HeadroomBytes: got %d want %d", got, 1*gb)
	}
}

func TestManager_SetStrategy_StaticSkipsKeepWarm(t *testing.T) {
	now := time.Now()
	m := newTestManager(8*gb, 6*gb, 2*gb, 0)
	m.SetStrategy(vram.StrategyStatic)
	m.SetLoadedModels([]vram.ModelAllocation{
		{Model: "warm", Bytes: 4 * gb, LastUsed: now.Add(-10 * time.Minute), KeepWarm: true},
		{Model: "cold", Bytes: 2 * gb, LastUsed: now.Add(-1 * time.Minute)},
	})

	ok, plan := m.CanFit("model-x", 4*gb)
	if !ok || plan == nil {
		t.Fatalf("expected fit with eviction plan, got ok=%v plan=%v", ok, plan)
	}
	if len(plan.ModelsToEvict) != 1 || plan.ModelsToEvict[0] != "cold" {
		t.Fatalf("static strategy should only evict non-warm models, got %v", plan.ModelsToEvict)
	}
}

func trackedBytes(state *vram.VRAMState) int64 {
	var total int64
	for _, m := range state.LoadedModels {
		total += m.Bytes
	}
	return total
}

// A manager with a real measurement refuses a model that cannot fit. This is
// the case that a fixed 16 GiB seed hid until 2026-08-13: on a 4 GiB card the
// check said yes, and Ollama then reported CUDA out-of-memory.
func TestManager_Metered_RefusesModelLargerThanDevice(t *testing.T) {
	m := newTestManager(4*gb, 0, 4*gb, 512*mb)
	if ok, _ := m.CanFit("qwen2.5-coder:14b", 9*gb); ok {
		t.Fatal("CanFit said a 9 GiB model fits on a 4 GiB device")
	}
	if err := m.Reserve("qwen2.5-coder:14b", 9*gb); err == nil {
		t.Fatal("Reserve accepted a 9 GiB model on a 4 GiB device")
	}
}

// An unmetered manager has no true device size, so it states no opinion on
// capacity. It must still keep the record of what is loaded — the dashboard
// and the eviction order both read it.
func TestManager_Unmetered_PermitsAndStillRecords(t *testing.T) {
	m := vram.NewManager(vram.ManagerConfig{
		Strategy:  vram.StrategyDynamic,
		Unmetered: true,
	})
	if !m.Unmetered() {
		t.Fatal("Unmetered() reported false for an unmetered manager")
	}
	if ok, _ := m.CanFit("huge", 64*gb); !ok {
		t.Fatal("an unmetered manager refused a model")
	}
	if err := m.Reserve("huge", 64*gb); err != nil {
		t.Fatalf("an unmetered Reserve failed: %v", err)
	}
	state := m.GetState()
	if len(state.LoadedModels) != 1 || state.LoadedModels[0].Model != "huge" {
		t.Fatalf("an unmetered manager did not record the allocation: %+v", state.LoadedModels)
	}
	if err := m.Release("huge"); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if len(m.GetState().LoadedModels) != 0 {
		t.Fatal("Release did not remove the allocation")
	}
}
