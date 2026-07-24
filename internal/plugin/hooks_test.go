package plugin_test

import (
	"context"
	"sync"
	"testing"

	"github.com/glider-ai/glider/internal/plugin"
)

type hookProbe struct {
	plugin.Base
	mu     sync.Mutex
	enters []plugin.StageHook
	exits  []plugin.StageHook
}

func (p *hookProbe) Hooks() plugin.HookProvider { return p }

func (p *hookProbe) OnStageEnter(_ context.Context, h plugin.StageHook) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters = append(p.enters, h)
	return nil
}

func (p *hookProbe) OnStageExit(_ context.Context, h plugin.StageHook) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exits = append(p.exits, h)
	return nil
}

func TestCapHooksDispatch(t *testing.T) {
	r := plugin.NewMemRegistry(&plugin.SimpleHost{Root: t.TempDir()})
	p := &hookProbe{Base: plugin.Base{
		M:    plugin.Meta{ID: "hooks-probe", Name: "Hooks"},
		Caps: []plugin.Capability{plugin.CapHooks},
	}}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	_ = r.InitAll(context.Background(), nil)
	_ = r.StartAll(context.Background())

	plugin.DispatchStageEnter(context.Background(), r, plugin.StageHook{
		HoopID: "h1", StageID: "actor", StageKind: "actor", Iteration: 1,
	})
	plugin.DispatchStageExit(context.Background(), r, plugin.StageHook{
		HoopID: "h1", StageID: "actor", StageKind: "actor", Iteration: 1,
	})

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.enters) != 1 || p.enters[0].Phase != "enter" || p.enters[0].StageID != "actor" {
		t.Fatalf("enters=%+v", p.enters)
	}
	if len(p.exits) != 1 || p.exits[0].Phase != "exit" {
		t.Fatalf("exits=%+v", p.exits)
	}
}

func TestCapHooksNoopWithoutProviders(t *testing.T) {
	r := plugin.NewMemRegistry(nil)
	// Must not panic.
	plugin.DispatchStageEnter(context.Background(), nil, plugin.StageHook{})
	plugin.DispatchStageExit(context.Background(), r, plugin.StageHook{})
}
