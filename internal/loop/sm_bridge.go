package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/statemachine"
)

// BuildMachine converts a LoopSpec into the shared AI-first state machine def.
func BuildMachine(spec LoopSpec) (statemachine.MachineDef, error) {
	stages := make([]statemachine.LoopStageInput, 0, len(spec.Stages))
	for _, s := range EnabledStages(spec.Stages) {
		tools := make([]statemachine.ToolRef, 0, len(s.Tools))
		for _, t := range s.Tools {
			tools = append(tools, statemachine.ToolRef{
				Name: t.Name, Kind: t.Kind, Server: t.Server, Plugin: t.Plugin,
			})
		}
		stages = append(stages, statemachine.LoopStageInput{
			ID: s.ID, Kind: string(s.Kind), Name: s.Name,
			Parallel: s.Parallel, Roles: s.Roles, Tools: tools,
		})
	}
	edges := make([]statemachine.LoopEdgeInput, 0, len(spec.GraphEdges))
	for _, e := range spec.GraphEdges {
		edges = append(edges, statemachine.LoopEdgeInput{
			ID: e.ID, Source: e.Source, Target: e.Target, Kind: e.Kind,
		})
	}
	force := statemachine.Topology(spec.Topology)
	def, err := statemachine.FromLoopStages(spec.ID, spec.GraphVersion, stages, edges, force)
	if err != nil {
		return def, err
	}
	// Apply per-edge guard overrides from GraphEdge.
	byID := map[string]GraphEdge{}
	for _, e := range spec.GraphEdges {
		byID[e.ID] = e
		key := fmt.Sprintf("%s->%s:%s", e.Source, e.Target, e.Kind)
		byID[key] = e
	}
	for i := range def.Transitions {
		t := &def.Transitions[i]
		ge, ok := byID[t.ID]
		if !ok {
			ge, ok = byID[fmt.Sprintf("%s->%s:%s", t.From, t.To, t.Kind)]
		}
		if !ok {
			continue
		}
		if ge.Guard != "" {
			t.Guard.Kind = statemachine.GuardKind(ge.Guard)
		}
		if ge.Threshold > 0 {
			t.Guard.Threshold = ge.Threshold
			t.Guard.MinRelevancy = ge.Threshold
		}
		if ge.Label != "" {
			t.Label = ge.Label
		}
	}
	return def, def.Normalize()
}

// stageOrderFromMachine returns enabled stages in SM walk order (fallback: EnabledStages).
func stageOrderFromMachine(spec LoopSpec) ([]StageSpec, *statemachine.Runtime, error) {
	modules := EnabledStages(spec.Stages)
	if len(modules) == 0 {
		return modules, nil, nil
	}
	def, err := BuildMachine(spec)
	if err != nil {
		return modules, nil, err
	}
	rt, err := statemachine.NewRuntime(def)
	if err != nil {
		return modules, nil, err
	}
	byID := map[string]StageSpec{}
	for _, m := range modules {
		byID[m.ID] = m
		byID[string(m.Kind)] = m
	}
	order := statemachine.WalkOrder(def)
	var ordered []StageSpec
	seen := map[string]bool{}
	for _, id := range order {
		if m, ok := byID[string(id)]; ok {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			ordered = append(ordered, m)
		}
	}
	for _, m := range modules {
		if !seen[m.ID] {
			ordered = append(ordered, m)
		}
	}
	if len(ordered) == 0 {
		ordered = modules
	}
	return ordered, rt, nil
}

func progressFromRoute(rt *statemachine.Runtime, base CycleProgress) CycleProgress {
	if rt == nil {
		return base
	}
	r := rt.Route
	base.Current = string(r.Current)
	base.Topology = string(r.Topology)
	base.RouteStatus = string(r.Status)
	base.PathTaken = stateIDsToStrings(r.PathTaken)
	base.EdgesTaken = append([]string(nil), r.EdgesTaken...)
	base.NextEdges = append([]string(nil), r.NextEdges...)
	base.BranchChoices = nil
	for _, c := range r.BranchChoices {
		base.BranchChoices = append(base.BranchChoices, BranchChoiceJSON{
			EdgeID: c.EdgeID, To: string(c.To), Kind: c.Kind,
			Score: c.Score, Selected: c.Selected, Reason: c.Reason,
		})
	}
	if base.StageID == "" && r.Current != "" {
		base.StageID = string(r.Current)
	}
	return base
}

func stateIDsToStrings(in []statemachine.StateID) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

// SnapshotGraph returns a versioned JSON-friendly snapshot for audits.
func SnapshotGraph(spec LoopSpec) map[string]any {
	ver := spec.GraphVersion
	if ver == "" {
		ver = "1"
	}
	return map[string]any{
		"id":            spec.ID,
		"graph_version": ver,
		"topology":      spec.Topology,
		"stages":        spec.Stages,
		"graph_edges":   spec.GraphEdges,
		"updated_at":    spec.UpdatedAt,
	}
}

// pickFeedbackTarget finds a feedback/on_fail/escalate edge target from critic.
func pickFeedbackTarget(spec LoopSpec, evalScore float64, evalPass bool) string {
	def, err := BuildMachine(spec)
	if err != nil {
		return ""
	}
	criticID := ""
	for _, s := range spec.Stages {
		if s.Kind == StageCritic && s.IsEnabled() {
			criticID = s.ID
			break
		}
	}
	if criticID == "" {
		return ""
	}
	ctx := statemachine.DecisionContext{
		BudgetOK:  true,
		EvalScore: evalScore,
		EvalPass:  evalPass,
		Relevancy: statemachine.RelevancyFromSignals(evalScore, evalPass, string(spec.Route), 0),
	}
	cands := statemachine.RankOutgoing(def, statemachine.StateID(criticID), ctx)
	for _, c := range cands {
		if !c.Pass {
			continue
		}
		k := c.Transition.Kind
		if k == statemachine.EdgeFeedback || k == statemachine.EdgeOnFail || k == statemachine.EdgeEscalate {
			return string(c.Transition.To)
		}
	}
	return ""
}

// relevancyHint blends optional contextgraph activity into a 0..1 signal.
func relevancyHint(evalScore float64, evalPass bool, route RoutePref, eventCount int) float64 {
	graphHint := 0.0
	if eventCount > 0 {
		graphHint = 0.4 + float64(eventCount%10)*0.05
		if graphHint > 0.9 {
			graphHint = 0.9
		}
	}
	return statemachine.RelevancyFromSignals(evalScore, evalPass, string(route), graphHint)
}

func openGate(st *LoopState, mod StageSpec, reason string) {
	now := time.Now().UTC()
	st.Status = StatusWaitingHuman
	st.Gate = GateRequest{
		Active:    true,
		StageID:   mod.ID,
		StageKind: string(mod.Kind),
		Reason:    reason,
		Iteration: st.Iteration,
		OpenedAt:  now,
	}
	st.Checkpoint.EvalStatus = "waiting_human"
	st.Progress.Phase = "waiting_human"
	st.Progress.StageID = mod.ID
	st.Progress.StageKind = string(mod.Kind)
	st.Progress.Note = reason
	st.Progress.UpdatedAt = now
	st.UpdatedAt = now
}

func clearGateDecision(st *LoopState) {
	st.Gate.Active = false
}

func summarizeBranch(rt *statemachine.Runtime) string {
	if rt == nil {
		return ""
	}
	var parts []string
	for _, c := range rt.Route.BranchChoices {
		mark := " "
		if c.Selected {
			mark = "*"
		}
		parts = append(parts, fmt.Sprintf("%s%s:%.2f", mark, c.To, c.Score))
	}
	return strings.Join(parts, " ")
}
