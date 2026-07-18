package swarm_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/swarm"
)

func TestFanOutCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32
	block := make(chan struct{})
	workers := []swarm.Worker{
		{
			ID: "slow",
			Run: func(ctx context.Context) (contextkit.Episode, error) {
				started.Add(1)
				select {
				case <-ctx.Done():
					return contextkit.Episode{}, ctx.Err()
				case <-block:
					return contextkit.Episode{Summary: "done"}, nil
				}
			},
		},
		{
			ID: "also-slow",
			Run: func(ctx context.Context) (contextkit.Episode, error) {
				started.Add(1)
				select {
				case <-ctx.Done():
					return contextkit.Episode{}, ctx.Err()
				case <-block:
					return contextkit.Episode{Summary: "done"}, nil
				}
			},
		},
	}
	done := make(chan struct{})
	var results []swarm.Result
	var err error
	go func() {
		results, err = swarm.FanOut(ctx, workers, swarm.Options{MaxWorkers: 2})
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for started.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("workers did not start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("FanOut did not return after cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	for _, r := range results {
		if r.Err != nil && !errors.Is(r.Err, context.Canceled) {
			t.Fatalf("result err=%v", r.Err)
		}
	}
}

func TestMergeResultsCallable(t *testing.T) {
	ep := swarm.MergeResults([]swarm.Result{
		{WorkerID: "a", Episode: contextkit.Episode{Summary: "one", Tokens: 3}},
		{WorkerID: "b", Err: errors.New("fail")},
		{WorkerID: "c", Episode: contextkit.Episode{Summary: "two", Tokens: 4}},
	})
	if ep.Tokens != 7 {
		t.Fatalf("tokens=%d", ep.Tokens)
	}
	if ep.Summary == "" || ep.Model != "glider-swarm" {
		t.Fatalf("ep=%+v", ep)
	}
}

func TestGroupCancelSiblings(t *testing.T) {
	g, _ := swarm.WithContext(context.Background())
	var sawCancel atomic.Bool
	g.Go(func(ctx context.Context) error {
		return errors.New("boom")
	})
	g.Go(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			sawCancel.Store(true)
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	})
	err := g.Wait()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err=%v", err)
	}
	if !sawCancel.Load() {
		t.Fatal("sibling was not cancelled")
	}
}

func TestHotSwapRegistryApply(t *testing.T) {
	cfg := &config.Config{}
	p := config.NewProvider(cfg, "testdata-unused.yaml")
	reg := swarm.NewRegistry()
	var applied atomic.Int32
	_ = reg.Register(&swarm.Module{
		Name: "router",
		Kind: swarm.ModuleRouter,
		Hot:  true,
		Apply: func(c *config.Config) error {
			applied.Add(1)
			return nil
		},
	})
	_ = reg.Register(&swarm.Module{
		Name: "backend",
		Kind: swarm.ModuleBackend,
		Hot:  false,
		Apply: func(c *config.Config) error {
			t.Fatal("cold module must not Apply")
			return nil
		},
	})
	reg.BindProvider(p)
	p.Swap(&config.Config{})
	if applied.Load() != 1 {
		t.Fatalf("applied=%d", applied.Load())
	}
	if err := reg.SetEnabled("router", false); err != nil {
		t.Fatal(err)
	}
	p.Swap(&config.Config{})
	if applied.Load() != 1 {
		t.Fatalf("disabled module still applied: %d", applied.Load())
	}
	list := reg.List()
	if len(list) < 2 {
		t.Fatalf("list=%v", list)
	}
	if len(swarm.Docs()) < 3 {
		t.Fatal("Docs empty")
	}
	if len(swarm.BuiltinCatalog()) < 3 {
		t.Fatal("catalog empty")
	}
}

func TestSwarmRunnerMerge(t *testing.T) {
	r := &swarm.Runner{
		WorkerFn: func(ctx context.Context, role swarm.Role, model, prompt string) (contextkit.Episode, error) {
			return contextkit.Episode{Summary: string(role) + "-ok", Model: model, Tokens: 2}, nil
		},
	}
	r.SetEnabled(true)
	r.ApplyOpts(swarm.Options{MaxWorkers: 2, ResultChanSize: 8})
	out, err := r.Run(context.Background(), swarm.RunRequest{
		Prompt: "hello",
		Roles:  []string{"plan", "exec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary == "" || out.TurnID == "" || len(out.Results) != 2 {
		t.Fatalf("out=%+v", out)
	}
}

func TestTemplateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := swarm.NewTemplateStore(dir)
	tpl := &swarm.Template{
		ID: "dual", Name: "Dual", Prompt: "do work", Roles: []string{"plan", "exec"},
		Enabled: true, PreferLocal: true,
	}
	if err := store.Save(tpl); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("dual")
	if err != nil || got.Prompt != "do work" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestIntervalLoopTickAndStop(t *testing.T) {
	loop := &swarm.IntervalLoop{}
	ch, err := loop.Start(context.Background(), swarm.LoopJob{
		ID:       "j1",
		Interval: time.Hour,
		Checkpoint: contextkit.LoopCheckpoint{Goal: "test", EvalStatus: "pending"},
		Tick: func(ctx context.Context, cp contextkit.LoopCheckpoint) (contextkit.LoopCheckpoint, error) {
			cp.EvalStatus = "pass"
			return cp, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case tick := <-ch:
		if tick.Checkpoint.EvalStatus != "pass" {
			t.Fatalf("tick=%+v", tick)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tick")
	}
	loop.Stop()
}

func TestOptionsFromConfig(t *testing.T) {
	opts := swarm.OptionsFromConfig(config.OrchestrationConfig{
		FanOut:      config.FanOutConfig{MaxWorkers: 3},
		Concurrency: config.ConcurrencyConfig{ResultChanSize: 64, MaxInflight: 2},
	})
	if opts.MaxWorkers != 3 || opts.ResultChanSize != 64 || opts.MaxInflight != 2 {
		t.Fatalf("opts=%+v", opts)
	}
}
