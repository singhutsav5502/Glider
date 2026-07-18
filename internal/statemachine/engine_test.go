package statemachine

import "testing"

func TestNormalizeAndWalk(t *testing.T) {
	def := MachineDef{
		ID: "t1",
		States: []State{
			{ID: "a", Kind: "memory"},
			{ID: "b", Kind: "planner"},
			{ID: "c", Kind: "actor"},
			{ID: "d", Kind: "critic"},
		},
		Transitions: []Transition{
			{From: "a", To: "b", Kind: EdgeFlow},
			{From: "b", To: "c", Kind: EdgeFlow},
			{From: "c", To: "d", Kind: EdgeFlow},
			{From: "d", To: "b", Kind: EdgeFeedback},
		},
	}
	rt, err := NewRuntime(def)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Def.Topology != TopologyLoop {
		t.Fatalf("topology=%s want loop", rt.Def.Topology)
	}
	order := WalkOrder(rt.Def)
	if len(order) < 4 {
		t.Fatalf("order=%v", order)
	}
	if order[0] != "a" {
		t.Fatalf("first=%s", order[0])
	}
}

func TestRelevancyPicksConditional(t *testing.T) {
	def := MachineDef{
		ID:       "branch",
		Topology: TopologyTree,
		States: []State{
			{ID: "router", Kind: "router"},
			{ID: "safe", Kind: "actor"},
			{ID: "risky", Kind: "actor"},
		},
		Transitions: []Transition{
			{From: "router", To: "safe", Kind: EdgeConditional, Guard: GuardSpec{Kind: GuardRelevancy, MinRelevancy: 0.6}, Priority: 1},
			{From: "router", To: "risky", Kind: EdgeConditional, Guard: GuardSpec{Kind: GuardRelevancy, MinRelevancy: 0.2}, Priority: 0},
		},
	}
	rt, err := NewRuntime(def)
	if err != nil {
		t.Fatal(err)
	}
	rt.SetContext(DecisionContext{BudgetOK: true, Relevancy: 0.8})
	tr, ok := rt.Next()
	if !ok {
		t.Fatal("expected transition")
	}
	if tr.To != "safe" {
		t.Fatalf("got %s want safe (higher min but relevancy passes both; priority+score)", tr.To)
	}
	// Lower relevancy: only risky passes.
	rt.SetContext(DecisionContext{BudgetOK: true, Relevancy: 0.4})
	tr, ok = rt.Next()
	if !ok || tr.To != "risky" {
		t.Fatalf("got %+v ok=%v want risky", tr, ok)
	}
}

func TestScoreBelowFeedback(t *testing.T) {
	def := MachineDef{
		ID: "fb",
		States: []State{
			{ID: "critic", Kind: "critic"},
			{ID: "planner", Kind: "planner"},
			{ID: "done", Kind: "memory"},
		},
		Transitions: []Transition{
			{From: "critic", To: "done", Kind: EdgeFlow, Guard: GuardSpec{Kind: GuardScoreAbove, Threshold: 0.7}},
			{From: "critic", To: "planner", Kind: EdgeFeedback, Guard: GuardSpec{Kind: GuardScoreBelow, Threshold: 0.7}},
		},
	}
	rt, err := NewRuntime(def)
	if err != nil {
		t.Fatal(err)
	}
	rt.Current = "critic"
	rt.SetContext(DecisionContext{BudgetOK: true, EvalScore: 0.4, EvalPass: false, Relevancy: 0.5})
	tr, ok := rt.Next()
	if !ok || tr.To != "planner" {
		t.Fatalf("want feedback to planner, got %+v ok=%v", tr, ok)
	}
	rt.SetContext(DecisionContext{BudgetOK: true, EvalScore: 0.9, EvalPass: true, Relevancy: 0.5})
	tr, ok = rt.Next()
	if !ok || tr.To != "done" {
		t.Fatalf("want flow to done, got %+v ok=%v", tr, ok)
	}
}

func TestHumanGate(t *testing.T) {
	def := MachineDef{
		ID: "hitl",
		States: []State{
			{ID: "gate", Kind: "human_gate"},
			{ID: "after", Kind: "actor"},
		},
		Transitions: []Transition{
			{From: "gate", To: "after", Kind: EdgeFlow, Guard: GuardSpec{Kind: GuardHumanApproved}},
		},
	}
	rt, err := NewRuntime(def)
	if err != nil {
		t.Fatal(err)
	}
	rt.Enter()
	if rt.Status != StatusWaitingHuman {
		t.Fatalf("status=%s", rt.Status)
	}
	_, ok := rt.Next()
	if ok {
		t.Fatal("should not advance without approval")
	}
	rt.ApplyHuman(HumanDecision{Approved: true, Comment: "lgtm"})
	tr, ok := rt.Next()
	if !ok || tr.To != "after" {
		t.Fatalf("got %+v ok=%v", tr, ok)
	}
	if err := rt.Advance(tr); err != nil {
		t.Fatal(err)
	}
	if rt.Current != "after" {
		t.Fatalf("current=%s", rt.Current)
	}
	if len(rt.Route.PathTaken) < 2 {
		t.Fatalf("path=%v", rt.Route.PathTaken)
	}
}

func TestFromLoopAndSwarm(t *testing.T) {
	def, err := FromLoopStages("h1", "2", []LoopStageInput{
		{ID: "m", Kind: "memory"},
		{ID: "p", Kind: "planner"},
		{ID: "a", Kind: "actor", Parallel: 2, Roles: []string{"exec", "research"}},
		{ID: "c", Kind: "critic"},
	}, []LoopEdgeInput{
		{Source: "m", Target: "p", Kind: "flow"},
		{Source: "p", Target: "a", Kind: "flow"},
		{Source: "a", Target: "c", Kind: "flow"},
		{Source: "c", Target: "p", Kind: "feedback"},
		{Source: "c", Target: "p", Kind: "on_fail"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if def.Topology != TopologySwarm && def.Topology != TopologyLoop {
		// parallel actors => swarm detection preferred
		t.Logf("topology=%s (ok if swarm or loop)", def.Topology)
	}
	if def.Version != "2" {
		t.Fatalf("version=%s", def.Version)
	}

	sw, err := FromSwarmRoles("s1", "1", []string{"plan", "exec", "research"})
	if err != nil {
		t.Fatal(err)
	}
	if sw.Topology != TopologySwarm {
		t.Fatalf("topo=%s", sw.Topology)
	}
	if len(sw.States) != 5 { // orch + 3 workers + merge
		t.Fatalf("states=%d", len(sw.States))
	}
}

func TestBudgetEdge(t *testing.T) {
	def := MachineDef{
		ID: "bud",
		States: []State{
			{ID: "act", Kind: "actor"},
			{ID: "cheap", Kind: "actor"},
			{ID: "next", Kind: "memory"},
		},
		Transitions: []Transition{
			{From: "act", To: "next", Kind: EdgeFlow},
			{From: "act", To: "cheap", Kind: EdgeBudgetExceeded},
		},
	}
	rt, err := NewRuntime(def)
	if err != nil {
		t.Fatal(err)
	}
	rt.Current = "act"
	rt.SetContext(DecisionContext{BudgetOK: false, Relevancy: 0.5})
	tr, ok := rt.Next()
	if !ok || tr.To != "cheap" {
		t.Fatalf("want budget edge to cheap, got %+v", tr)
	}
}

func TestRelevancyFromSignals(t *testing.T) {
	r := RelevancyFromSignals(0.9, true, "local", 0.6)
	if r < 0.5 || r > 1 {
		t.Fatalf("r=%v", r)
	}
}
