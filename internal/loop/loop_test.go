package loop

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
)

type mockCompleter struct {
	fn func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

func (m *mockCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return m.fn(r, req)
}

func (m *mockCompleter) CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return m.fn(r, req)
}

func streamText(s string) <-chan backend.CompletionChunk {
	ch := make(chan backend.CompletionChunk, 1)
	ch <- backend.CompletionChunk{Content: s, FinishReason: "stop"}
	close(ch)
	return ch
}

func TestNormalizeStagesAndPrompt(t *testing.T) {
	stages, err := NormalizeStages(nil)
	if err != nil || len(stages) != 6 {
		t.Fatalf("default stages len=%d err=%v", len(stages), err)
	}
	p := StagePrompt("ship fix", StageSpec{Kind: StageCritic, Prompt: "Grade it."}, "prior note", EvalSpec{Goal: "tests pass"})
	if !strings.Contains(p, "Eval: tests pass") || !strings.Contains(p, "[stage:critic]") {
		t.Fatalf("prompt=%q", p)
	}
}

func TestSpecNormalizeWithStages(t *testing.T) {
	s := LoopSpec{
		Prompt: "triage",
		Stages: []StageSpec{{Kind: StagePlanner}, {Kind: StageActor}},
		Eval:   EvalSpec{Goal: "done", OnSuccessN: 2},
	}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Goal != "triage" {
		t.Fatalf("goal=%q", s.Goal)
	}
	if s.Stop.OnSuccessN != 2 {
		t.Fatalf("stop not merged from eval: %+v", s.Stop)
	}
	if len(EnabledStages(s.Stages)) != 2 {
		t.Fatalf("enabled=%v", EnabledStages(s.Stages))
	}
	if s.Autonomy != AutonomyL1 {
		t.Fatalf("autonomy=%s", s.Autonomy)
	}
}

func TestParseEvalScore(t *testing.T) {
	if v := parseEvalScore("SCORE: 0.85\nREASON: ok"); v < 0.84 || v > 0.86 {
		t.Fatalf("score=%v", v)
	}
}

func TestScheduleEveryAndCron(t *testing.T) {
	s, err := parseSchedule("@every 2s", "")
	if err != nil || s.interval != 2*time.Second {
		t.Fatalf("every: %+v err=%v", s, err)
	}
	s2, err := parseSchedule("*/5 * * * *", "")
	if err != nil || s2.cron == nil {
		t.Fatalf("cron: %+v err=%v", s2, err)
	}
	from := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	next := s2.cron.nextAfter(from)
	if next.Minute()%5 != 0 {
		t.Fatalf("next=%v", next)
	}
}

func TestStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	spec := LoopSpec{ID: "t1", Prompt: "ping", Interval: "1s", Route: RouteLocal}
	if err := spec.Normalize(); err != nil {
		t.Fatal(err)
	}
	st := CreateState(spec)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("t1")
	if err != nil || got.Spec.Prompt != "ping" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if err := store.Delete("t1"); err != nil {
		t.Fatal(err)
	}
}

func TestHoopLearningBiasesLocal(t *testing.T) {
	st := &LoopState{
		Outcomes: []IterationOutcome{
			{Success: true, Route: "local", EvalScore: 0.9},
			{Success: true, Route: "local", EvalScore: 0.8},
			{Success: true, Route: "local", EvalScore: 0.85},
		},
	}
	cfg := HoopLearningConfig{Enabled: true, LocalBiasStep: 0.1, MaxBias: 0.5, Window: 20}
	ApplyHoopLearning(st, cfg)
	if st.Hoop.LocalBias <= 0 {
		t.Fatalf("bias=%v", st.Hoop.LocalBias)
	}
	route := EffectiveRoute(LoopSpec{Route: RouteAuto}, st.Hoop, cfg)
	if route != RouteLocal {
		t.Fatalf("route=%s", route)
	}
}

func TestManagerEngineeringCycle(t *testing.T) {
	dir := t.TempDir()
	var calls int
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		calls++
		if req == nil || len(req.Messages) == 0 {
			t.Fatal("empty messages")
		}
		content := req.Messages[0].Content
		if !strings.Contains(content, "/local") {
			t.Fatalf("want /local prefix, got %q", content)
		}
		// Critic stage asks for SCORE
		if strings.Contains(content, "checker") || strings.Contains(content, "SCORE:") {
			return streamText("SCORE: 0.95\nREASON: goal met"), nil
		}
		return streamText("plan or act output DONE"), nil
	}}
	g := contextgraph.New("")
	mgr := NewManager(NewStore(dir), mc, g, RunnerConfig{
		DefaultRoute: RouteLocal,
		Hoop:         HoopLearningConfig{Enabled: true},
	})
	st, err := mgr.Create(LoopSpec{
		ID:            "job1",
		Goal:          "ping",
		Prompt:        "ping",
		MaxIterations: 1,
		Route:         RouteLocal,
		FailPolicy:    FailContinue,
		Learning:      true,
		Autonomy:      AutonomyL2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Spec.Stages) < 3 {
		t.Fatalf("expected default stages, got %d", len(st.Spec.Stages))
	}
	if _, err := mgr.Start(context.Background(), st.Spec.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		cur, err := mgr.Get("job1")
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == StatusCompleted || cur.Status == StatusFailed || cur.Status == StatusStopped {
			if cur.Iteration < 1 {
				t.Fatalf("iteration=%d status=%s", cur.Iteration, cur.Status)
			}
			if cur.LastEvalScore < 0.9 {
				t.Fatalf("eval=%v outcomes=%+v", cur.LastEvalScore, cur.Outcomes)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%s iter=%d", cur.Status, cur.Iteration)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	// planner + actor + critic at minimum
	if calls < 3 {
		t.Fatalf("calls=%d want >= 3 (planner/actor/critic)", calls)
	}
	evs := g.RecentEvents(50)
	var sawTick bool
	for _, ev := range evs {
		if ev.Kind == contextgraph.EventLoopTick {
			sawTick = true
		}
	}
	if !sawTick {
		t.Fatal("expected LoopTick event")
	}
}

func TestManagerFailStop(t *testing.T) {
	dir := t.TempDir()
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		return nil, errors.New("backend down")
	}}
	mgr := NewManager(NewStore(dir), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	_, err := mgr.Create(LoopSpec{
		ID: "fail1", Prompt: "x", Route: RouteLocal, FailPolicy: FailStop, MaxIterations: 1,
		Stages: []StageSpec{{Kind: StageActor, Prompt: "do it"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), "fail1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		cur, _ := mgr.Get("fail1")
		if cur != nil && cur.Status == StatusFailed {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status=%v", cur)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCatalog(t *testing.T) {
	c := Catalog()
	if len(c.Kinds) != 5 || len(c.Defaults) < 3 {
		t.Fatalf("%+v", c)
	}
}

func TestDefaultDir(t *testing.T) {
	d := DefaultDir()
	if !strings.Contains(filepath.ToSlash(d), ".glider/loops") && !strings.Contains(d, string(os.PathSeparator)+".glider"+string(os.PathSeparator)+"loops") {
		t.Fatalf("dir=%s", d)
	}
}
