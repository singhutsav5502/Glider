package backend_test

import (
	"context"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
)

type stubBackend struct {
	name string
	typ  backend.BackendType
}

func (s *stubBackend) Name() string              { return s.name }
func (s *stubBackend) Type() backend.BackendType { return s.typ }
func (s *stubBackend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	ch := make(chan backend.CompletionChunk)
	close(ch)
	return ch, nil
}

// T1.6.1 — Register and retrieve backend
func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := backend.NewRegistry()
	b := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	if err := reg.Register(b); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := reg.Get("ollama")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != b {
		t.Fatalf("expected same instance")
	}
}

// T1.6.2 — Get unknown backend returns error
func TestRegistry_GetUnknown(t *testing.T) {
	reg := backend.NewRegistry()
	_ = reg.Register(&stubBackend{name: "ollama", typ: backend.BackendTypeLocal})
	got, err := reg.Get("nonexistent")
	if err == nil || got != nil {
		t.Fatalf("expected error and nil, got %v, %v", got, err)
	}
}

// T1.6.3 — List all backends
func TestRegistry_List(t *testing.T) {
	reg := backend.NewRegistry()
	for _, n := range []string{"ollama", "vllm", "openai"} {
		if err := reg.Register(&stubBackend{name: n, typ: backend.BackendTypeLocal}); err != nil {
			t.Fatal(err)
		}
	}
	list := reg.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 backends, got %d", len(list))
	}
	names := map[string]bool{}
	for _, b := range list {
		names[b.Name()] = true
	}
	for _, n := range []string{"ollama", "vllm", "openai"} {
		if !names[n] {
			t.Fatalf("missing backend %s", n)
		}
	}
}

// T1.6.4 — Register duplicate name returns error
func TestRegistry_DuplicateRegister(t *testing.T) {
	reg := backend.NewRegistry()
	orig := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	if err := reg.Register(orig); err != nil {
		t.Fatal(err)
	}
	other := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	if err := reg.Register(other); err == nil {
		t.Fatal("expected duplicate error")
	}
	got, err := reg.Get("ollama")
	if err != nil || got != orig {
		t.Fatalf("original should be preserved")
	}
}

// T3.2.1 — Register model with metadata
func TestRegistry_RegisterModel(t *testing.T) {
	reg := backend.NewRegistry()
	info := backend.ModelInfo{
		Name:           "codellama:7b",
		VRAMEstimateMB: 4200,
		MaxContext:     16384,
		Capabilities:   []string{"code"},
	}
	if err := reg.RegisterModel(info); err != nil {
		t.Fatal(err)
	}
	got, err := reg.GetModel("codellama:7b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "codellama:7b" || got.VRAMEstimateMB != 4200 || got.MaxContext != 16384 {
		t.Fatalf("metadata mismatch: %+v", got)
	}
	if got.State != backend.ModelStateCold {
		t.Fatalf("expected COLD default, got %s", got.State)
	}
}

// T3.2.2 — Track model state
func TestRegistry_SetModelState(t *testing.T) {
	reg := backend.NewRegistry()
	_ = reg.RegisterModel(backend.ModelInfo{Name: "codellama:7b"})
	if err := reg.SetModelState("codellama:7b", backend.ModelStateWarm); err != nil {
		t.Fatal(err)
	}
	got, _ := reg.GetModel("codellama:7b")
	if got.State != backend.ModelStateWarm {
		t.Fatalf("expected WARM, got %s", got.State)
	}
}

// T3.2.3 — Find model by capability
func TestRegistry_FindByCapability(t *testing.T) {
	reg := backend.NewRegistry()
	_ = reg.RegisterModel(backend.ModelInfo{Name: "code", Capabilities: []string{"code", "refactor"}})
	_ = reg.RegisterModel(backend.ModelInfo{Name: "docs", Capabilities: []string{"general", "docs"}})
	found := reg.FindByCapability("code")
	if len(found) != 1 || found[0].Name != "code" {
		t.Fatalf("expected only code model, got %+v", found)
	}
}
