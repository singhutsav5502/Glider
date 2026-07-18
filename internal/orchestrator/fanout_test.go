package orchestrator_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/orchestrator"
)

type stubExec struct {
	content string
	err     error
	block   <-chan struct{}
	started *atomic.Int32
}

func (s *stubExec) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	if s.started != nil {
		s.started.Add(1)
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.block:
		}
	}
	ch := make(chan backend.CompletionChunk, 1)
	model := req.Model
	if decision != nil && decision.Model != "" {
		model = decision.Model
	}
	ch <- backend.CompletionChunk{Content: s.content, FinishReason: "stop", Model: model}
	close(ch)
	return ch, nil
}

func TestFanOutDisabledReturnsError(t *testing.T) {
	e := &orchestrator.FanOutExecutor{
		Inner:  &stubExec{content: "x"},
		Config: orchestrator.FanOutConfig{Enabled: false},
	}
	_, err := e.Execute(context.Background(), &backend.RoutingDecision{
		Strategy: backend.StrategyFanOut,
		Target:   "local",
		Model:    "m",
	}, &backend.CompletionRequest{Model: "m"})
	if !errors.Is(err, orchestrator.ErrFanOutDisabled) {
		t.Fatalf("err=%v", err)
	}
}

func TestFanOutEnabledMergesWorkers(t *testing.T) {
	store := contextkit.NewStore(8)
	e := &orchestrator.FanOutExecutor{
		Inner: &stubExec{content: "hello"},
		Config: orchestrator.FanOutConfig{
			Enabled:      true,
			MaxWorkers:   2,
			EpisodeStore: store,
			SessionID:    "test",
		},
	}
	ch, err := e.Execute(context.Background(), &backend.RoutingDecision{
		Strategy: backend.StrategyFanOut,
		Target:   "local",
		Model:    "m1",
		SubTasks: []backend.SubTask{{Model: "m1"}, {Model: "m2"}},
	}, &backend.CompletionRequest{Model: "m1", Messages: []backend.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for range ch {
		n++
	}
	if n < 2 {
		t.Fatalf("chunks=%d want >=2", n)
	}
	st := store.Get("test")
	if len(st.Episodes) != 1 {
		t.Fatalf("episodes=%d", len(st.Episodes))
	}
}

func TestFanOutDelegatesSingle(t *testing.T) {
	e := &orchestrator.FanOutExecutor{
		Inner:  &stubExec{content: "solo"},
		Config: orchestrator.FanOutConfig{Enabled: false},
	}
	ch, err := e.Execute(context.Background(), &backend.RoutingDecision{
		Strategy: backend.StrategySingle,
		Target:   "local",
		Model:    "m",
	}, &backend.CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for c := range ch {
		got += c.Content
	}
	if got != "solo" {
		t.Fatalf("got=%q", got)
	}
}

func TestFanOutCancelStopsWorkers(t *testing.T) {
	block := make(chan struct{})
	var started atomic.Int32
	e := &orchestrator.FanOutExecutor{
		Inner: &stubExec{content: "x", block: block, started: &started},
		Config: orchestrator.FanOutConfig{
			Enabled:    true,
			MaxWorkers: 2,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ch, err := e.Execute(ctx, &backend.RoutingDecision{
			Strategy: backend.StrategyFanOut,
			Target:   "local",
			Model:    "m",
			SubTasks: []backend.SubTask{{Model: "a"}, {Model: "b"}},
		}, &backend.CompletionRequest{Model: "m"})
		if err != nil {
			t.Errorf("execute err=%v", err)
		} else {
			for range ch {
			}
		}
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for started.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("worker did not start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after cancel")
	}
}
