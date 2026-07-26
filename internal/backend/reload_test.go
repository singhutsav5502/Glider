package backend_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

func TestRegistry_ReplaceAll_PreservesStateAndSwaps(t *testing.T) {
	reg := backend.NewRegistry()
	old := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	if err := reg.Register(old); err != nil {
		t.Fatal(err)
	}
	_ = reg.RegisterModel(backend.ModelInfo{Name: "m1", Backend: "ollama"})
	_ = reg.SetModelState("m1", backend.ModelStateWarm)

	next := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	reg.ReplaceAll(map[string]backend.InferenceBackend{
		"ollama": next,
		"vllm":   &stubBackend{name: "vllm", typ: backend.BackendTypeLocal},
	}, []backend.ModelInfo{
		{Name: "m1", Backend: "ollama"},
		{Name: "m2", Backend: "vllm"},
	})

	got, err := reg.Get("ollama")
	if err != nil || got != next {
		t.Fatalf("expected swapped ollama client, got %v err=%v", got, err)
	}
	if _, err := reg.Get("vllm"); err != nil {
		t.Fatal(err)
	}
	m1, err := reg.GetModel("m1")
	if err != nil {
		t.Fatal(err)
	}
	if m1.State != backend.ModelStateWarm {
		t.Fatalf("expected preserved WARM, got %s", m1.State)
	}
	m2, err := reg.GetModel("m2")
	if err != nil {
		t.Fatal(err)
	}
	if m2.State != backend.ModelStateCold {
		t.Fatalf("expected COLD default for new model, got %s", m2.State)
	}
}

func TestReloader_SuccessSwapsClients(t *testing.T) {
	reg := backend.NewRegistry()
	old := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	_ = reg.Register(old)

	next := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	reloader := &backend.Reloader{
		Registry: reg,
		Build: func(cfg *config.Config) (*backend.Snapshot, error) {
			return &backend.Snapshot{
				Backends: map[string]backend.InferenceBackend{"ollama": next},
				Models:   []backend.ModelInfo{{Name: "codellama:7b", Backend: "ollama"}},
			}, nil
		},
	}
	cfg := config.DefaultConfig()
	if err := reloader.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get("ollama")
	if err != nil || got != next {
		t.Fatalf("expected new client after reload")
	}
	st := reloader.Status()
	if !st.OK || !st.Attempted {
		t.Fatalf("status=%+v", st)
	}
}

func TestReloader_FailureLeavesPreviousWorking(t *testing.T) {
	reg := backend.NewRegistry()
	old := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	_ = reg.Register(old)
	_ = reg.RegisterModel(backend.ModelInfo{Name: "m1", Backend: "ollama"})

	reloader := &backend.Reloader{
		Registry: reg,
		Build: func(cfg *config.Config) (*backend.Snapshot, error) {
			return nil, fmt.Errorf("synthetic build failure")
		},
	}
	if err := reloader.Apply(config.DefaultConfig()); err == nil {
		t.Fatal("expected error")
	}
	got, err := reg.Get("ollama")
	if err != nil || got != old {
		t.Fatalf("previous backend must remain after failed reload")
	}
	if _, err := reg.GetModel("m1"); err != nil {
		t.Fatal("previous model must remain")
	}
	st := reloader.Status()
	if st.OK || !st.Attempted || st.Error == "" {
		t.Fatalf("expected failed status, got %+v", st)
	}
}

func TestReloader_UnknownModelBackendFailsWithoutSwap(t *testing.T) {
	reg := backend.NewRegistry()
	old := &stubBackend{name: "ollama", typ: backend.BackendTypeLocal}
	_ = reg.Register(old)

	reloader := &backend.Reloader{
		Registry: reg,
		Build: func(cfg *config.Config) (*backend.Snapshot, error) {
			return &backend.Snapshot{
				Backends: map[string]backend.InferenceBackend{
					"ollama": &stubBackend{name: "ollama", typ: backend.BackendTypeLocal},
				},
				Models: []backend.ModelInfo{{Name: "x", Backend: "missing"}},
			}, nil
		},
	}
	if err := reloader.Apply(config.DefaultConfig()); err == nil {
		t.Fatal("expected validation error")
	}
	got, _ := reg.Get("ollama")
	if got != old {
		t.Fatal("registry must not swap on validation failure")
	}
}

func TestReloader_AfterSwapRunsOnlyOnSuccess(t *testing.T) {
	reg := backend.NewRegistry()
	_ = reg.Register(&stubBackend{name: "ollama", typ: backend.BackendTypeLocal})
	var calls int
	reloader := &backend.Reloader{
		Registry: reg,
		Build: func(cfg *config.Config) (*backend.Snapshot, error) {
			return nil, fmt.Errorf("nope")
		},
		AfterSwap: func(cfg *config.Config) { calls++ },
	}
	_ = reloader.Apply(config.DefaultConfig())
	if calls != 0 {
		t.Fatalf("AfterSwap must not run on failure, calls=%d", calls)
	}

	reloader.Build = func(cfg *config.Config) (*backend.Snapshot, error) {
		return &backend.Snapshot{
			Backends: map[string]backend.InferenceBackend{
				"ollama": &stubBackend{name: "ollama", typ: backend.BackendTypeLocal},
			},
		}, nil
	}
	if err := reloader.Apply(config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("AfterSwap calls=%d", calls)
	}
	// ensure Status.At is set
	if reloader.Status().At.IsZero() || time.Since(reloader.Status().At) > time.Minute {
		t.Fatalf("unexpected At=%v", reloader.Status().At)
	}
}
