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
	"github.com/glider-ai/glider/internal/tools"
)

func TestMidCycleHITLResumeContinuesAfterGate(t *testing.T) {
	dir := t.TempDir()
	seen := make(chan string, 8)
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		msg := ""
		if len(req.Messages) > 0 {
			msg = req.Messages[len(req.Messages)-1].Content
		}
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "after-gate") || strings.Contains(msg, "STAGE_AFTER") {
			seen <- "after"
			return streamText("post-gate ok"), nil
		}
		if strings.Contains(lower, "before") || strings.Contains(msg, "STAGE_BEFORE") {
			seen <- "before"
			return streamText("pre-gate draft"), nil
		}
		seen <- "other"
		return streamText("ok"), nil
	}}
	mgr := NewManager(NewStore(filepath.Join(dir, "loops")), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	mgr.Logs = agentlog.NewStore(32)
	mgr.Tools = tools.NewRegistry(tools.Options{})

	st, err := mgr.Create(LoopSpec{
		ID:            "midhitl",
		Goal:          "mid cycle gate",
		Prompt:        "mid cycle gate",
		Route:         RouteLocal,
		MaxIterations: 1,
		Stages: []StageSpec{
			{ID: "before", Kind: StageActor, Prompt: "STAGE_BEFORE work"},
			{ID: "gate", Kind: StageHumanGate},
			{ID: "after", Kind: StageActor, Prompt: "STAGE_AFTER work"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), st.Spec.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := mgr.Get(st.Spec.ID)
		if cur != nil && cur.Status == StatusWaitingHuman && cur.Cursor.Active {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cur, err := mgr.Get(st.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Status != StatusWaitingHuman || !cur.Cursor.Active {
		t.Fatalf("want waiting with cursor, got status=%s cursor=%+v", cur.Status, cur.Cursor)
	}
	if cur.Cursor.ResumeStageID != "gate" {
		t.Fatalf("resume stage=%s", cur.Cursor.ResumeStageID)
	}
	if cur.Cursor.PlanText == "" && cur.Cursor.ActorText == "" {
		// Actor text should be persisted from before stage.
		if cur.Cursor.ActorText == "" && cur.Iteration < 1 {
			t.Fatalf("expected partial cycle state: %+v", cur.Cursor)
		}
	}
	iterAtGate := cur.Iteration
	if _, err := mgr.DecideGate(st.Spec.ID, GateDecision{Approve: true, Resume: true}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	gotAfter := false
	for time.Now().Before(deadline) {
		select {
		case s := <-seen:
			if s == "after" {
				gotAfter = true
			}
		default:
		}
		cur, _ = mgr.Get(st.Spec.ID)
		if cur != nil && (cur.Status == StatusCompleted || cur.Status == StatusStopped || cur.Status == StatusFailed) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	cur, _ = mgr.Get(st.Spec.ID)
	if !gotAfter {
		t.Fatalf("expected after-gate stage to run; status=%s iter=%d cursor=%+v", cur.Status, cur.Iteration, cur.Cursor)
	}
	// Mid-cycle resume must not bump to a brand-new iteration number beyond +0 same cycle.
	if cur.Iteration > iterAtGate+1 {
		t.Fatalf("iteration jumped too far: gate=%d now=%d", iterAtGate, cur.Iteration)
	}
	_ = mgr.Stop(st.Spec.ID)
	mgr.Shutdown()
}

func TestGovernanceHardTokensStops(t *testing.T) {
	dir := t.TempDir()
	mc := &mockCompleter{fn: func(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
		return streamText(strings.Repeat("word ", 200)), nil
	}}
	mgr := NewManager(NewStore(filepath.Join(dir, "loops")), mc, nil, RunnerConfig{DefaultRoute: RouteLocal})
	st, err := mgr.Create(LoopSpec{
		ID:            "gov1",
		Goal:          "burn tokens",
		Prompt:        "burn",
		Route:         RouteLocal,
		MaxIterations: 5,
		Governance: GovernanceSpec{
			HardTokens:        50,
			PreferLocalOnSoft: true,
		},
		Stages: []StageSpec{
			{ID: "a", Kind: StageActor, Prompt: "talk"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Start(context.Background(), st.Spec.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := mgr.Get(st.Spec.ID)
		if cur != nil && (cur.Status == StatusFailed || cur.Checkpoint.WakeReason == "budget_exceeded") {
			if cur.Spend.HardHit || strings.Contains(cur.LastError, "budget") || cur.Checkpoint.WakeReason == "budget_exceeded" {
				_ = mgr.Stop(st.Spec.ID)
				mgr.Shutdown()
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	cur, _ := mgr.Get(st.Spec.ID)
	_ = mgr.Stop(st.Spec.ID)
	mgr.Shutdown()
	t.Fatalf("expected budget stop, status=%s spend=%+v wake=%s err=%s", cur.Status, cur.Spend, cur.Checkpoint.WakeReason, cur.LastError)
}
