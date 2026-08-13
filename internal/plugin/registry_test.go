package plugin

import (
	"context"
	"testing"
)

type echoTools struct{}

func (echoTools) ListTools(context.Context) ([]ToolSchema, error) {
	return []ToolSchema{{Name: "echo", Description: "echo"}}, nil
}
func (echoTools) CallTool(_ context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{Name: call.Name, OK: true, Content: "echo:" + call.RawInput}, nil
}

type echoPlugin struct {
	Base
	tp echoTools
}

func (p *echoPlugin) Tools() ToolProvider { return p.tp }

func TestMemRegistryLifecycle(t *testing.T) {
	r := NewMemRegistry(&SimpleHost{Root: t.TempDir()})
	p := &echoPlugin{Base: Base{M: Meta{ID: "echo", Name: "Echo", Version: "1"}, Caps: []Capability{CapTools}}}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.InitAll(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := r.HealthAll(context.Background())
	if !h["echo"].OK {
		t.Fatalf("%+v", h)
	}
	tps := r.ToolProviders()
	if len(tps) != 1 {
		t.Fatalf("%d", len(tps))
	}
	tr, err := tps[0].CallTool(context.Background(), ToolCall{Name: "echo", RawInput: "hi"})
	if err != nil || tr.Content != "echo:hi" {
		t.Fatalf("%+v err=%v", tr, err)
	}
	_ = r.StopAll(context.Background())
}
