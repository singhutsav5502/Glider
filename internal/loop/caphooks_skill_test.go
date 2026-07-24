package loop

import (
	"context"
	"sync"
	"testing"

	"github.com/glider-ai/glider/internal/plugin"
)

type stageHookProbe struct {
	plugin.Base
	mu     sync.Mutex
	enters []plugin.StageHook
	exits  []plugin.StageHook
}

func (p *stageHookProbe) Hooks() plugin.HookProvider { return p }

func (p *stageHookProbe) OnStageEnter(_ context.Context, h plugin.StageHook) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters = append(p.enters, h)
	return nil
}

func (p *stageHookProbe) OnStageExit(_ context.Context, h plugin.StageHook) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exits = append(p.exits, h)
	return nil
}

func TestManagerCapHooksDispatch(t *testing.T) {
	reg := plugin.NewMemRegistry(&plugin.SimpleHost{Root: t.TempDir()})
	probe := &stageHookProbe{Base: plugin.Base{
		M:    plugin.Meta{ID: "loop-hooks", Name: "LoopHooks"},
		Caps: []plugin.Capability{plugin.CapHooks},
	}}
	if err := reg.Register(probe); err != nil {
		t.Fatal(err)
	}
	_ = reg.InitAll(context.Background(), nil)
	_ = reg.StartAll(context.Background())

	m := &Manager{Plugins: reg}
	hook := plugin.StageHook{
		HoopID: "h1", StageID: "risky", StageKind: "actor", Iteration: 1,
		Attrs: map[string]string{"autonomy": "L1"},
	}
	plugin.DispatchStageEnter(context.Background(), m.Plugins, hook)
	plugin.DispatchStageExit(context.Background(), m.Plugins, hook)

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.enters) != 1 || probe.enters[0].StageID != "risky" {
		t.Fatalf("enters=%+v", probe.enters)
	}
	if probe.enters[0].Attrs["autonomy"] != "L1" {
		t.Fatalf("attrs=%v", probe.enters[0].Attrs)
	}
	if len(probe.exits) != 1 || probe.exits[0].Phase != "exit" {
		t.Fatalf("exits=%+v", probe.exits)
	}
}

func TestStageAutonomyL3Inherit(t *testing.T) {
	spec := LoopSpec{Autonomy: AutonomyL3}
	mod := ModuleSpec{Kind: StageActor, ID: "a"}
	if stageAutonomy(spec, mod) != AutonomyL3 {
		t.Fatal("should inherit L3")
	}
	mod.Autonomy = AutonomyL1
	if stageAutonomy(spec, mod) != AutonomyL1 {
		t.Fatal("stage override")
	}
}
