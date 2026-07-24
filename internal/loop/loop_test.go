package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/swarm"
	"github.com/glider-ai/glider/internal/tools"
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
	if _, ok := parseEvalScoreOK("Please share the contents of the files so I can score."); ok {
		t.Fatal("chatty critic without SCORE must not parse as scored")
	}
	v, ok := parseEvalScoreOK("SCORE: 0.0\nREASON: empty actor")
	if !ok || v != 0 {
		t.Fatalf("explicit SCORE 0: v=%v ok=%v", v, ok)
	}
	// Mid-sentence floats / FastAPI prose must not count as SCORE.
	for _, c := range []string{
		"Please share the FastAPI files so I can review them.",
		"The audit uses threshold 0.75 in docs.",
		"score is roughly 0.9 but I cannot confirm",
	} {
		if _, ok := parseEvalScoreOK(c); ok {
			t.Fatalf("prose without SCORE line must fail: %q", c)
		}
	}
	// Markdown / header SCORE variants (cycle #15: model bolded SCORE and gate rejected it).
	for _, c := range []string{
		"**SCORE: 0.8**\nREASON: solid",
		"*SCORE: 0.8*\nREASON: solid",
		"  **SCORE: 0.75**  ",
		"## SCORE: 0.9",
		"### **SCORE: 0.65**",
	} {
		got, ok := parseEvalScoreOK(c)
		if !ok {
			t.Fatalf("markdown SCORE must parse: %q", c)
		}
		if got < 0.6 || got > 1.0 {
			t.Fatalf("markdown SCORE out of range: %q -> %v", c, got)
		}
	}
	if v, ok := parseEvalScoreOK("**SCORE: 0.8**"); !ok || v < 0.79 || v > 0.81 {
		t.Fatalf("bold SCORE 0.8: v=%v ok=%v", v, ok)
	}
	// Structured JSON critic output (Ollama format / json mode).
	for _, c := range []string{
		`{"score":0.8,"reason":"ok"}`,
		"```json\n{\"score\": 0.55, \"reason\": \"partial\"}\n```",
		"Here is my verdict:\n{\"score\":1.0,\"reason\":\"goal met\"}\n",
	} {
		got, ok := parseEvalScoreOK(c)
		if !ok {
			t.Fatalf("JSON critic must parse: %q", c)
		}
		if got < 0 || got > 1 {
			t.Fatalf("JSON score out of range: %q -> %v", c, got)
		}
	}
	if v, ok := parseEvalScoreOK(`{"score":0.8,"reason":"ok"}`); !ok || v < 0.79 || v > 0.81 {
		t.Fatalf("JSON score 0.8: v=%v ok=%v", v, ok)
	}
	// JSON without explicit score key must not pass.
	if _, ok := parseEvalScoreOK(`{"reason":"looks fine","confidence":0.9}`); ok {
		t.Fatal("JSON without score key must not parse")
	}
	norm := normalizeCriticOutput(`{"score":0.42,"reason":"needs work"}`)
	if !strings.Contains(norm, "SCORE:") || !strings.Contains(norm, "REASON:") {
		t.Fatalf("normalizeCriticOutput: %q", norm)
	}
	if v := parseEvalScore(norm); v < 0.41 || v > 0.43 {
		t.Fatalf("normalized score=%v", v)
	}
}

func TestDefaultCriticPromptForbidsQuestions(t *testing.T) {
	lower := strings.ToLower(defaultCriticPrompt)
	if !strings.Contains(lower, "do not ask") {
		t.Fatal("defaultCriticPrompt must forbid questions")
	}
	if !strings.Contains(defaultCriticPrompt, "SCORE:") {
		t.Fatal("defaultCriticPrompt must require SCORE")
	}
	if !strings.Contains(defaultCriticPrompt, `"score"`) {
		t.Fatal("defaultCriticPrompt must allow JSON {score,reason}")
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
	if len(c.Kinds) != 7 || len(c.Defaults) < 3 {
		t.Fatalf("%+v", c)
	}
}

func TestDefaultDir(t *testing.T) {
	d := DefaultDir()
	if !strings.Contains(filepath.ToSlash(d), ".glider/loops") && !strings.Contains(d, string(os.PathSeparator)+".glider"+string(os.PathSeparator)+"loops") {
		t.Fatalf("dir=%s", d)
	}
}

func TestGraphEdgesNormalize(t *testing.T) {
	s := LoopSpec{
		Goal:   "g",
		Stages: []StageSpec{{Kind: StageActor}},
		GraphEdges: []GraphEdge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "a", Kind: "feedback"},
		},
	}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.GraphEdges[0].Kind != "flow" || s.GraphEdges[0].ID == "" {
		t.Fatalf("%+v", s.GraphEdges[0])
	}
	if s.GraphEdges[1].Kind != "feedback" {
		t.Fatalf("%+v", s.GraphEdges[1])
	}
}

func TestMaxLatencyStop(t *testing.T) {
	dir := t.TempDir()
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		time.Sleep(30 * time.Millisecond)
		return streamText("ok"), nil
	}}
	mgr := NewManager(NewStore(dir), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	_, err := mgr.Create(LoopSpec{
		ID: "lat1", Prompt: "x", Route: RouteLocal, FailPolicy: FailContinue, MaxIterations: 5,
		Stages: []StageSpec{{Kind: StageActor, Prompt: "do"}},
		Stop:   StopConditions{MaxLatencyMS: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), "lat1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		cur, _ := mgr.Get("lat1")
		if cur != nil && (cur.Status == StatusFailed || cur.Status == StatusCompleted || cur.Status == StatusStopped) {
			if cur.Checkpoint.WakeReason != "max_latency" && cur.Status != StatusFailed {
				t.Fatalf("want max_latency fail, got status=%s wake=%s", cur.Status, cur.Checkpoint.WakeReason)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%v", cur)
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestParallelActorFanOut(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		n := calls.Add(1)
		content := req.Messages[0].Content
		if strings.Contains(content, "checker") || strings.Contains(content, "SCORE:") {
			return streamText("SCORE: 0.9\nREASON: ok"), nil
		}
		return streamText(fmt.Sprintf("worker-out-%d DONE", n)), nil
	}}
	mgr := NewManager(NewStore(dir), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	_, err := mgr.Create(LoopSpec{
		ID: "par1", Goal: "parallel", Prompt: "parallel", Route: RouteLocal,
		FailPolicy: FailContinue, MaxIterations: 1, Autonomy: AutonomyL2,
		Stages: []StageSpec{
			{Kind: StageActor, ID: "actor", Prompt: "act", Parallel: 2, Roles: []string{"exec", "research"}},
			{Kind: StageCritic, ID: "critic", EvalMin: 0.5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	if _, err := mgr.Start(context.Background(), "par1"); err != nil {
		t.Fatal(err)
	}
	for {
		cur, _ := mgr.Get("par1")
		if cur != nil && cur.Status != StatusRunning && cur.Status != StatusIdle {
			if calls.Load() < 3 { // 2 workers + critic
				t.Fatalf("calls=%d want >= 3", calls.Load())
			}
			if len(cur.Outcomes) < 1 {
				t.Fatal("no outcomes")
			}
			var sawActor bool
			for _, s := range cur.Outcomes[0].Stages {
				if s.Kind == "actor" && s.Success {
					sawActor = true
					if !strings.Contains(s.Summary, "critique") && !strings.Contains(s.Summary, "worker") && !strings.Contains(s.Summary, "DONE") {
						t.Fatalf("actor summary=%q", s.Summary)
					}
				}
			}
			if !sawActor {
				t.Fatalf("no actor stage in %+v", cur.Outcomes[0].Stages)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%v calls=%d", cur, calls.Load())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestNormalizeParallelModeAndContext(t *testing.T) {
	s := StageSpec{Kind: StageActor, ID: "a", Parallel: 2}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.ParallelMode != ParallelModeFanout {
		t.Fatalf("parallel_mode=%q want fanout", s.ParallelMode)
	}
	s2 := StageSpec{
		Kind: StageActor, ID: "b", Parallel: 2, ParallelMode: "SWARM",
		Swarm: &StageSwarmSpec{
			TemplateID:  " t1 ",
			Waves:       2,
			WeavePolicy: " Critic ",
			PreferLocal: true,
			Models:      []string{" m1 ", "m2"},
		},
	}
	if err := s2.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s2.ParallelMode != ParallelModeSwarm || s2.Swarm.TemplateID != "t1" || s2.Swarm.Waves != 2 {
		t.Fatalf("%+v", s2)
	}
	if s2.Swarm.WeavePolicy != "critic" || !s2.Swarm.PreferLocal || s2.Swarm.Models[0] != "m1" {
		t.Fatalf("swarm knobs=%+v", s2.Swarm)
	}
	ctx := StageSpec{Kind: StageContext, ID: "ctx"}
	if err := ctx.Normalize(); err != nil {
		t.Fatal(err)
	}
	bad := StageSpec{Kind: StageActor, ID: "bad", ParallelMode: "weird"}
	if err := bad.Normalize(); err == nil {
		t.Fatal("expected bad parallel_mode error")
	}
}

func TestParallelModeJSONRoundTrip(t *testing.T) {
	in := StageSpec{
		Kind: StageActor, ID: "actor", Parallel: 2, ParallelMode: ParallelModeSwarm,
		Roles: []string{"exec", "research"},
		Swarm: &StageSwarmSpec{
			TemplateID: "tpl", Waves: 2, WeavePolicy: "critic",
			PreferLocal: true, Models: []string{"m1", "m2"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out StageSpec
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Normalize(); err != nil {
		t.Fatal(err)
	}
	if out.ParallelMode != ParallelModeSwarm || out.Parallel != 2 {
		t.Fatalf("%+v", out)
	}
	if out.Swarm == nil || out.Swarm.TemplateID != "tpl" || out.Swarm.Waves != 2 {
		t.Fatalf("swarm=%+v", out.Swarm)
	}
	if out.Swarm.WeavePolicy != "critic" || !out.Swarm.PreferLocal || len(out.Swarm.Models) != 2 {
		t.Fatalf("swarm knobs=%+v", out.Swarm)
	}
}

func TestParallelModeSwarm(t *testing.T) {
	dir := t.TempDir()
	var swarmCalls atomic.Int32
	runner := &swarm.Runner{DefaultModel: "test"}
	runner.SetEnabled(true)
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		content := req.Messages[0].Content
		if strings.Contains(content, "checker") || strings.Contains(content, "SCORE:") {
			return streamText("SCORE: 0.9\nREASON: ok"), nil
		}
		// Fanout path must not be used; fail loudly if Completer is hit for actor.
		return streamText("UNEXPECTED_FANOUT"), nil
	}}
	g := contextgraph.New("")
	mgr := NewManager(NewStore(dir), mc, g, RunnerConfig{DefaultRoute: RouteLocal})
	mgr.Swarm = runner
	reg := tools.NewRegistry(tools.Options{})
	reg.SetRunID("pre-swarm-run")
	mgr.Tools = reg
	var sawNestedRunID atomic.Bool
	runner.WorkerFn = func(ctx context.Context, role swarm.Role, model, prompt string) (contextkit.Episode, error) {
		if id := reg.RunID(); id != "" && id != "pre-swarm-run" && strings.Contains(id, "loop_") {
			sawNestedRunID.Store(true)
		}
		n := swarmCalls.Add(1)
		return contextkit.Episode{
			Summary: fmt.Sprintf("swarm-out-%d DONE role=%s", n, role),
			Tokens:  12,
			Model:   model,
			Role:    string(role),
		}, nil
	}
	_, err := mgr.Create(LoopSpec{
		ID: "swarm-par", Goal: "swarm parallel", Prompt: "swarm", Route: RouteLocal,
		FailPolicy: FailContinue, MaxIterations: 1, Autonomy: AutonomyL2,
		Stages: []StageSpec{
			{
				Kind: StageActor, ID: "actor", Prompt: "act via swarm",
				Parallel: 2, ParallelMode: ParallelModeSwarm, Roles: []string{"exec", "research"},
			},
			{Kind: StageCritic, ID: "critic", EvalMin: 0.5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), "swarm-par"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		cur, _ := mgr.Get("swarm-par")
		if cur != nil && cur.Status != StatusRunning && cur.Status != StatusIdle {
			if swarmCalls.Load() < 2 {
				t.Fatalf("swarm calls=%d want >= 2 status=%s", swarmCalls.Load(), cur.Status)
			}
			if !sawNestedRunID.Load() {
				t.Fatal("expected nested Tools.SetRunID during swarm workers")
			}
			// Cycle EnsureRunLayout sets hoop id; nested swarm must restore to that (not leave nested turn).
			if reg.RunID() != "swarm-par" {
				t.Fatalf("Tools.RunID not restored to hoop id: %q", reg.RunID())
			}
			if len(cur.Outcomes) < 1 {
				t.Fatal("no outcomes")
			}
			var sawActor bool
			for _, s := range cur.Outcomes[0].Stages {
				if s.Kind == "actor" {
					sawActor = true
					if !s.Success {
						t.Fatalf("actor failed: %s", s.Err)
					}
					if !strings.Contains(s.Summary, "swarm-out") && !strings.Contains(s.Summary, "DONE") {
						t.Fatalf("actor summary=%q", s.Summary)
					}
				}
			}
			if !sawActor {
				t.Fatalf("no actor in %+v", cur.Outcomes[0].Stages)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%v swarmCalls=%d", cur, swarmCalls.Load())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestParallelModeSwarmUnavailable(t *testing.T) {
	dir := t.TempDir()
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		return streamText("should-not-run"), nil
	}}
	mgr := NewManager(NewStore(dir), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	// Swarm nil → explicit error, no silent fanout fallback.
	_, err := mgr.Create(LoopSpec{
		ID: "swarm-miss", Goal: "x", Prompt: "x", Route: RouteLocal,
		FailPolicy: FailStop, MaxIterations: 1, Autonomy: AutonomyL2,
		Stages: []StageSpec{
			{Kind: StageActor, ID: "actor", Prompt: "act", Parallel: 2, ParallelMode: ParallelModeSwarm},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), "swarm-miss"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		cur, _ := mgr.Get("swarm-miss")
		if cur != nil && (cur.Status == StatusFailed || cur.Status == StatusStopped || cur.Status == StatusCompleted) {
			if cur.Status == StatusCompleted {
				t.Fatal("expected failure when swarm unavailable")
			}
			found := false
			if len(cur.Outcomes) > 0 {
				for _, s := range cur.Outcomes[0].Stages {
					if strings.Contains(s.Err, "swarm") {
						found = true
					}
				}
			}
			if !found && !strings.Contains(cur.LastError, "swarm") {
				t.Fatalf("want swarm error, got status=%s last=%q outcomes=%+v", cur.Status, cur.LastError, cur.Outcomes)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%v", cur)
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestContextStageSeedsGraph(t *testing.T) {
	dir := t.TempDir()
	g := contextgraph.New("")
	var actorSawDigest atomic.Bool
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		content := req.Messages[0].Content
		if strings.Contains(content, "SCORE:") || strings.Contains(content, "checker") {
			return streamText("SCORE: 0.95\nREASON: ok"), nil
		}
		if strings.Contains(content, "[context_digest]") && strings.Contains(content, "CONTEXT:") {
			actorSawDigest.Store(true)
		}
		return streamText("ACTOR_DONE"), nil
	}}
	mgr := NewManager(NewStore(dir), mc, g, RunnerConfig{DefaultRoute: RouteLocal})
	_, err := mgr.Create(LoopSpec{
		ID: "ctx1", Goal: "seed context goal", Prompt: "do", Route: RouteLocal,
		FailPolicy: FailContinue, MaxIterations: 1, Autonomy: AutonomyL2,
		Stages: []StageSpec{
			{Kind: StagePlanner, ID: "planner", Prompt: "plan it"},
			{Kind: StageContext, ID: "context_seed"},
			{Kind: StageActor, ID: "actor", Prompt: "act"},
			{Kind: StageCritic, ID: "critic", EvalMin: 0.5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), "ctx1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		cur, _ := mgr.Get("ctx1")
		if cur != nil && cur.Status != StatusRunning && cur.Status != StatusIdle {
			ents := g.Entities("loop:ctx1", 32)
			if len(ents) == 0 {
				t.Fatal("expected contextgraph entities after context stage")
			}
			q := g.Query("loop:ctx1", "goal", 8)
			if q == "" || strings.Contains(strings.ToLower(q), "no hits") {
				t.Fatalf("expected goal in query, got %q", q)
			}
			if v, ok := g.LookupHoopContext("loop:ctx1", "goal"); !ok || !strings.Contains(v, "seed context goal") {
				t.Fatalf("LookupHoopContext goal=%q ok=%v", v, ok)
			}
			if !actorSawDigest.Load() {
				t.Fatal("actor prompt missing CONTEXT / [context_digest]")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout status=%v", cur)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestHoopLearningStagePrefs(t *testing.T) {
	st := &LoopState{
		Outcomes: []IterationOutcome{
			{
				Success: true, Route: "local", EvalScore: 0.9,
				Stages: []StageOutcome{
					{Kind: "planner", Success: true},
					{Kind: "actor", Success: true},
					{Kind: "critic", Success: true},
				},
			},
			{
				Success: false, Route: "local", EvalScore: 0.2,
				Stages: []StageOutcome{
					{Kind: "planner", Success: true},
					{Kind: "actor", Success: false},
					{Kind: "critic", Success: true},
				},
			},
		},
	}
	ApplyHoopLearning(st, HoopLearningConfig{Enabled: true, LocalBiasStep: 0.1, MaxBias: 0.5, Window: 20})
	if st.Hoop.StagePrefs["planner"] <= 0 {
		t.Fatalf("prefs=%v", st.Hoop.StagePrefs)
	}
	if len(st.Hoop.PreferredStages) == 0 {
		t.Fatal("expected preferred stages")
	}
}

func TestEvaluateStopUsesPersistedCounters(t *testing.T) {
	mgr := &Manager{}
	st := &LoopState{
		Spec:          LoopSpec{Stop: StopConditions{OnSuccessN: 2}},
		ConsecutiveOK: 2,
	}
	reason := mgr.evaluateStop(st, IterationOutcome{Success: true}, "ok")
	if reason != "on_success_n" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestEvaluateStopMaxLatency(t *testing.T) {
	mgr := &Manager{}
	st := &LoopState{
		Spec: LoopSpec{Stop: StopConditions{MaxLatencyMS: 100}},
	}
	reason := mgr.evaluateStop(st, IterationOutcome{Success: true, LatencyMS: 500}, "ok")
	if reason != "max_latency" {
		t.Fatalf("reason=%q want max_latency", reason)
	}
}

func TestApplyDefaultMaxTokens(t *testing.T) {
	mgr := &Manager{Cfg: RunnerConfig{}}
	req := &backend.CompletionRequest{}
	mgr.applyDefaultMaxTokens(req)
	if req.MaxTokens == nil || *req.MaxTokens != DefaultCompletionMaxTokens {
		t.Fatalf("default MaxTokens=%v want %d", req.MaxTokens, DefaultCompletionMaxTokens)
	}
	n := 12000
	req2 := &backend.CompletionRequest{MaxTokens: &n}
	mgr.applyDefaultMaxTokens(req2)
	if *req2.MaxTokens != 12000 {
		t.Fatal("must not overwrite explicit MaxTokens")
	}
	mgr2 := &Manager{Cfg: RunnerConfig{DefaultMaxTokens: 4096}}
	req3 := &backend.CompletionRequest{}
	mgr2.applyDefaultMaxTokens(req3)
	if req3.MaxTokens == nil || *req3.MaxTokens != 4096 {
		t.Fatalf("cfg override=%v", req3.MaxTokens)
	}
}

func TestStageTextCapsRaisedForAudits(t *testing.T) {
	if stageTextCap < 32000 || stageOutcomeCap < 16000 || promptActorCap < 12000 {
		t.Fatalf("caps too low: text=%d outcome=%d actor=%d", stageTextCap, stageOutcomeCap, promptActorCap)
	}
}
