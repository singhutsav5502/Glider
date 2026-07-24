package statemachine

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// EvaluateGuard returns whether the transition may fire, a score for ranking, and reason.
// AI-first: relevancy and score guards use DecisionContext; heuristics fill gaps.
func EvaluateGuard(g GuardSpec, edge EdgeKind, ctx DecisionContext) (pass bool, score float64, reason string) {
	kind := g.Kind
	if kind == "" {
		kind = defaultGuardForEdge(edge)
	}
	rel := ctx.Relevancy
	if rel <= 0 {
		rel = 0.5 // neutral fallback when no signal
	}
	switch kind {
	case GuardAlways:
		return true, rel + priorityBoost(edge), "always"
	case GuardScoreBelow:
		th := g.Threshold
		if th <= 0 {
			th = 0.7
		}
		pass = !ctx.EvalPass && (ctx.EvalScore < th || (ctx.EvalScore == 0 && ctx.LastError != ""))
		score = th - ctx.EvalScore
		if pass {
			return true, score + 0.1, fmt.Sprintf("score_below %.3f < %.3f", ctx.EvalScore, th)
		}
		return false, score, fmt.Sprintf("score not below %.3f", th)
	case GuardScoreAbove:
		th := g.Threshold
		if th <= 0 {
			th = 0.7
		}
		pass = ctx.EvalPass || ctx.EvalScore >= th
		score = ctx.EvalScore
		if pass {
			return true, score, fmt.Sprintf("score_above %.3f >= %.3f", ctx.EvalScore, th)
		}
		return false, score, "score too low"
	case GuardRelevancy:
		min := g.MinRelevancy
		if min <= 0 {
			min = g.Threshold
		}
		if min <= 0 {
			min = 0.3
		}
		pass = rel >= min
		score = rel
		if pass {
			return true, score, fmt.Sprintf("relevancy %.3f >= %.3f", rel, min)
		}
		return false, score, "relevancy too low"
	case GuardHeuristic:
		switch edge {
		case EdgeFeedback, EdgeOnFail, EdgeEscalate:
			pass = !ctx.EvalPass
		case EdgeBudgetExceeded:
			pass = !ctx.BudgetOK
		default:
			pass = ctx.EvalPass || ctx.EvalScore == 0
		}
		score = rel
		if pass {
			return true, score, "heuristic"
		}
		return false, score, "heuristic reject"
	case GuardPolicy:
		expr := strings.ToLower(strings.TrimSpace(g.Expr))
		switch {
		case strings.Contains(expr, "budget"):
			pass = !ctx.BudgetOK
		case expr == "cloud" || expr == "local" || expr == "auto":
			pass = strings.EqualFold(ctx.RouterSignal, expr)
		default:
			pass = ctx.BudgetOK
		}
		score = rel
		if pass {
			return true, score, "policy:" + expr
		}
		return false, score, "policy reject"
	case GuardHumanApproved:
		if ctx.Human != nil && ctx.Human.Approved {
			return true, 1.0, "human_approved"
		}
		return false, 0, "awaiting human"
	case GuardBudgetExceeded:
		pass = !ctx.BudgetOK
		if pass {
			return true, 0.9, "budget_exceeded"
		}
		return false, 0, "budget ok"
	default:
		return true, rel, "default"
	}
}

func priorityBoost(edge EdgeKind) float64 {
	switch edge {
	case EdgeFlow:
		return 0.05
	case EdgeParallel, EdgeMerge:
		return 0.04
	case EdgeConditional:
		return 0.03
	default:
		return 0
	}
}

// Candidate is a scored outgoing transition.
type Candidate struct {
	Transition Transition
	Pass       bool
	Score      float64
	Reason     string
}

// RankOutgoing evaluates and sorts outgoing transitions (best first).
func RankOutgoing(def MachineDef, from StateID, ctx DecisionContext) []Candidate {
	out := def.Outgoing(from)
	cands := make([]Candidate, 0, len(out))
	for _, t := range out {
		pass, score, reason := EvaluateGuard(t.Guard, t.Kind, ctx)
		score += float64(t.Priority) * 0.01
		cands = append(cands, Candidate{Transition: t, Pass: pass, Score: score, Reason: reason})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Pass != cands[j].Pass {
			return cands[i].Pass
		}
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Transition.Priority > cands[j].Transition.Priority
	})
	return cands
}

// NewRuntime starts at Initial.
func NewRuntime(def MachineDef) (*Runtime, error) {
	if err := def.Normalize(); err != nil {
		return nil, err
	}
	rt := &Runtime{
		Def:     def,
		Current: def.Initial,
		Status:  StatusIdle,
		Path:    []StateID{def.Initial},
		Context: DecisionContext{BudgetOK: true, Relevancy: 0.5},
	}
	rt.refreshRoute("init")
	return rt, nil
}

func (rt *Runtime) refreshRoute(note string) {
	if rt == nil {
		return
	}
	out := rt.Def.Outgoing(rt.Current)
	nextIDs := make([]string, 0, len(out))
	choices := make([]BranchChoice, 0, len(out))
	for _, t := range out {
		_, score, reason := EvaluateGuard(t.Guard, t.Kind, rt.Context)
		nextIDs = append(nextIDs, t.ID)
		choices = append(choices, BranchChoice{
			EdgeID: t.ID,
			To:     t.To,
			Kind:   string(t.Kind),
			Score:  score,
			Reason: reason,
		})
	}
	rt.Route = DecisionRoute{
		Current:       rt.Current,
		PathTaken:     append([]StateID(nil), rt.Path...),
		EdgesTaken:    append([]string(nil), rt.EdgesTaken...),
		NextEdges:     nextIDs,
		BranchChoices: choices,
		Topology:      rt.Def.Topology,
		Status:        rt.Status,
		Note:          note,
		UpdatedAt:     time.Now().UTC(),
	}
}

// Next picks the best passing transition (AI-first ranking). Returns false if none.
func (rt *Runtime) Next() (Transition, bool) {
	if rt == nil {
		return Transition{}, false
	}
	cands := RankOutgoing(rt.Def, rt.Current, rt.Context)
	choices := make([]BranchChoice, 0, len(cands))
	var chosen Transition
	found := false
	for _, c := range cands {
		sel := false
		if c.Pass && !found {
			chosen = c.Transition
			found = true
			sel = true
		}
		choices = append(choices, BranchChoice{
			EdgeID:   c.Transition.ID,
			To:       c.Transition.To,
			Kind:     string(c.Transition.Kind),
			Score:    c.Score,
			Selected: sel,
			Reason:   c.Reason,
		})
	}
	rt.Route.BranchChoices = choices
	rt.Route.NextEdges = nil
	for _, c := range cands {
		if c.Pass {
			rt.Route.NextEdges = append(rt.Route.NextEdges, c.Transition.ID)
		}
	}
	rt.Route.UpdatedAt = time.Now().UTC()
	rt.Route.Current = rt.Current
	rt.Route.Status = rt.Status
	return chosen, found
}

// Advance moves along tr and updates path / route.
func (rt *Runtime) Advance(tr Transition) error {
	if rt == nil {
		return fmt.Errorf("nil runtime")
	}
	if tr.From != "" && tr.From != rt.Current {
		return fmt.Errorf("transition from %s but current is %s", tr.From, rt.Current)
	}
	if rt.Def.StateByID(tr.To) == nil {
		return fmt.Errorf("unknown target %s", tr.To)
	}
	rt.Current = tr.To
	rt.Path = append(rt.Path, tr.To)
	rt.EdgesTaken = append(rt.EdgesTaken, tr.ID)
	rt.Status = StatusRunning
	st := rt.Def.StateByID(tr.To)
	note := "advance:" + string(tr.Kind)
	if st != nil && st.Kind == "human_gate" {
		rt.Status = StatusWaitingHuman
		rt.WaitingAt = tr.To
		note = "waiting_human"
	}
	rt.refreshRoute(note)
	for i := range rt.Route.BranchChoices {
		rt.Route.BranchChoices[i].Selected = rt.Route.BranchChoices[i].EdgeID == tr.ID
	}
	return nil
}

// Enter marks the machine running at current (or initial) without advancing.
func (rt *Runtime) Enter() {
	if rt == nil {
		return
	}
	rt.Status = StatusRunning
	st := rt.Def.StateByID(rt.Current)
	if st != nil && st.Kind == "human_gate" {
		rt.Status = StatusWaitingHuman
		rt.WaitingAt = rt.Current
		rt.refreshRoute("waiting_human")
		return
	}
	rt.refreshRoute("enter")
}

// ApplyHuman records a HITL decision and unlocks GuardHumanApproved edges.
func (rt *Runtime) ApplyHuman(d HumanDecision) {
	if rt == nil {
		return
	}
	d.At = time.Now().UTC()
	if d.GateNode == "" {
		d.GateNode = rt.WaitingAt
		if d.GateNode == "" {
			d.GateNode = rt.Current
		}
	}
	rt.PendingHuman = &d
	rt.Context.Human = &d
	if d.Approved {
		rt.Status = StatusRunning
		rt.WaitingAt = ""
	} else {
		rt.Status = StatusFailed
	}
	rt.refreshRoute("human_decision")
}

// SetContext replaces decision signals used by AI-first guards.
func (rt *Runtime) SetContext(ctx DecisionContext) {
	if rt == nil {
		return
	}
	rt.Context = ctx
	if rt.Context.Relevancy <= 0 {
		rt.Context.Relevancy = 0.5
	}
	rt.refreshRoute("context")
}

// WalkOrder returns a linear execution order preferring flow edges, falling back
// to state declaration order. Used when adapters need a deterministic stage list
// while still recording DecisionRoute for live viz.
func WalkOrder(def MachineDef) []StateID {
	_ = def.Normalize()
	if len(def.States) == 0 {
		return nil
	}
	visited := map[StateID]bool{}
	var order []StateID
	var dfs func(StateID)
	dfs = func(id StateID) {
		if visited[id] {
			return
		}
		visited[id] = true
		order = append(order, id)
		outs := RankOutgoing(def, id, DecisionContext{BudgetOK: true, Relevancy: 0.5, EvalPass: true})
		for _, c := range outs {
			if c.Transition.Kind == EdgeFeedback || c.Transition.Kind == EdgeOnFail ||
				c.Transition.Kind == EdgeEscalate || c.Transition.Kind == EdgeBudgetExceeded ||
				c.Transition.Kind == EdgeFeeds {
				continue // cycles / failure / data-feed paths not in primary walk
			}
			dfs(c.Transition.To)
		}
	}
	dfs(def.Initial)
	for _, s := range def.States {
		if !visited[s.ID] {
			order = append(order, s.ID)
			visited[s.ID] = true
		}
	}
	return order
}
