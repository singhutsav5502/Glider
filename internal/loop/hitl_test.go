package loop

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/statemachine"
	"github.com/glider-ai/glider/internal/tools"
)

func TestBuildMachineConditionalEdges(t *testing.T) {
	spec := LoopSpec{
		ID:           "m1",
		Goal:         "test",
		Prompt:       "test",
		GraphVersion: "3",
		Stages: []StageSpec{
			{ID: "r", Kind: StageRouter},
			{ID: "a", Kind: StageActor},
			{ID: "c", Kind: StageCritic, EvalMin: 0.8},
			{ID: "g", Kind: StageHumanGate},
		},
		GraphEdges: []GraphEdge{
			{Source: "r", Target: "a", Kind: "flow"},
			{Source: "a", Target: "c", Kind: "flow"},
			{Source: "c", Target: "g", Kind: "on_fail", Guard: "score_below", Threshold: 0.8},
			{Source: "c", Target: "a", Kind: "feedback"},
			{Source: "c", Target: "a", Kind: "conditional", Guard: "relevancy", Threshold: 0.5},
		},
	}
	if err := spec.Normalize(); err != nil {
		t.Fatal(err)
	}
	def, err := BuildMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if def.Version != "3" {
		t.Fatalf("version=%s", def.Version)
	}
	if def.Topology != statemachine.TopologyLoop && def.Topology != statemachine.TopologyGraph {
		t.Fatalf("topology=%s", def.Topology)
	}
	found := false
	for _, tr := range def.Transitions {
		if tr.Kind == statemachine.EdgeOnFail {
			found = true
			if tr.Guard.Kind != statemachine.GuardScoreBelow {
				t.Fatalf("guard=%s", tr.Guard.Kind)
			}
		}
	}
	if !found {
		t.Fatal("missing on_fail")
	}
	snap := SnapshotGraph(spec)
	if snap["graph_version"] != "3" {
		t.Fatalf("%v", snap)
	}
}

func TestHITLDecideAndResume(t *testing.T) {
	dir := t.TempDir()
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		msg := ""
		if len(req.Messages) > 0 {
			msg = req.Messages[len(req.Messages)-1].Content
		}
		if strings.Contains(strings.ToLower(msg), "checker") || strings.Contains(msg, "SCORE") || strings.Contains(msg, "ACTOR_OUTPUT") {
			return streamText("SCORE: 0.2\nREASON: no"), nil
		}
		return streamText("actor draft"), nil
	}}
	mgr := NewManager(NewStore(filepath.Join(dir, "loops")), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	mgr.Logs = agentlog.NewStore(32)
	mgr.Tools = tools.NewRegistry(tools.Options{})

	st, err := mgr.Create(LoopSpec{
		ID:        "hitl1",
		Goal:      "need approval",
		Prompt:    "need approval",
		Route:     RouteLocal,
		Autonomy:  AutonomyL1,
		HumanGate: true,
		MaxIterations: 2,
		Stages: []StageSpec{
			{ID: "actor", Kind: StageActor, Prompt: "do it"},
			{ID: "critic", Kind: StageCritic, EvalMin: 0.9, Prompt: defaultCriticPrompt},
		},
		Eval: EvalSpec{MinScore: 0.9},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.Start(context.Background(), st.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := mgr.Get(st.Spec.ID)
		if cur != nil && cur.Status == StatusWaitingHuman {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	cur, err := mgr.Get(st.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Status != StatusWaitingHuman {
		t.Fatalf("status=%s gate=%+v outcomes=%d", cur.Status, cur.Gate, len(cur.Outcomes))
	}
	if !cur.Gate.Active {
		t.Fatal("expected active gate")
	}
	// Approve without auto-resume first.
	cur, err = mgr.DecideGate(st.Spec.ID, GateDecision{Approve: true, Comment: "ok", Resume: false})
	if err != nil {
		t.Fatal(err)
	}
	if cur.Gate.Decision != "approve" {
		t.Fatalf("%+v", cur.Gate)
	}
	cur, err = mgr.Resume(context.Background(), st.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Status != StatusRunning && cur.Status != StatusCompleted && cur.Status != StatusWaitingHuman && cur.Status != StatusFailed && cur.Status != StatusStopped {
		t.Fatalf("unexpected status after resume: %s", cur.Status)
	}
}

func TestPickFeedbackTarget(t *testing.T) {
	spec := LoopSpec{
		ID:     "fb",
		Goal:   "g",
		Prompt: "g",
		Stages: []StageSpec{
			{ID: "planner", Kind: StagePlanner},
			{ID: "critic", Kind: StageCritic},
		},
		GraphEdges: []GraphEdge{
			{Source: "planner", Target: "critic", Kind: "flow"},
			{Source: "critic", Target: "planner", Kind: "feedback"},
		},
	}
	_ = spec.Normalize()
	target := pickFeedbackTarget(spec, 0.2, false)
	if target != "planner" {
		t.Fatalf("target=%s", target)
	}
}
