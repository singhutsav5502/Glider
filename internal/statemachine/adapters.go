package statemachine

import (
	"fmt"
	"strings"
)

// LoopStageInput is a minimal stage view for FromLoopSpec (avoids importing loop).
type LoopStageInput struct {
	ID       string
	Kind     string
	Name     string
	Parallel int
	Roles    []string
	Tools    []ToolRef
	Disabled bool
}

// LoopEdgeInput is a minimal graph edge view.
type LoopEdgeInput struct {
	ID     string
	Source string
	Target string
	Kind   string
}

// FromLoopStages builds a MachineDef from hoop stages + graph edges.
// Topology is DetectTopology unless forced.
func FromLoopStages(id, version string, stages []LoopStageInput, edges []LoopEdgeInput, force Topology) (MachineDef, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return MachineDef{}, fmt.Errorf("loop id required")
	}
	var states []State
	hasParallel := false
	for _, s := range stages {
		if s.Disabled {
			continue
		}
		sid := strings.TrimSpace(s.ID)
		if sid == "" {
			sid = strings.TrimSpace(s.Name)
		}
		if sid == "" {
			sid = strings.TrimSpace(s.Kind)
		}
		if sid == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(s.Kind))
		label := strings.TrimSpace(s.Name)
		if label == "" {
			label = sid
		}
		st := State{ID: StateID(sid), Kind: kind, Label: label, Tools: s.Tools}
		if s.Parallel > 1 {
			hasParallel = true
			if st.Meta == nil {
				st.Meta = map[string]string{}
			}
			st.Meta["parallel"] = fmt.Sprintf("%d", s.Parallel)
			if len(s.Roles) > 0 {
				st.Meta["roles"] = strings.Join(s.Roles, ",")
			}
		}
		states = append(states, st)
	}
	if len(states) == 0 {
		return MachineDef{}, fmt.Errorf("no enabled stages")
	}

	var transitions []Transition
	if len(edges) > 0 {
		for _, e := range edges {
			kind := EdgeKind(strings.ToLower(strings.TrimSpace(e.Kind)))
			if kind == "" {
				kind = EdgeFlow
			}
			if !ValidEdgeKind(kind) {
				kind = EdgeFlow
			}
			tr := Transition{
				ID:   e.ID,
				From: StateID(strings.TrimSpace(e.Source)),
				To:   StateID(strings.TrimSpace(e.Target)),
				Kind: kind,
			}
			transitions = append(transitions, tr)
		}
	} else {
		// Linear flow between consecutive stages.
		for i := 0; i+1 < len(states); i++ {
			transitions = append(transitions, Transition{
				From: states[i].ID,
				To:   states[i+1].ID,
				Kind: EdgeFlow,
			})
		}
	}

	topo := force
	if topo == "" {
		// Stage Parallel is a node capability, not TopologySwarm by itself.
		// Swarm topology requires parallel/merge *edges* (see FromSwarmRoles).
		_ = hasParallel
		topo = DetectTopology(transitions, false)
	}
	def := MachineDef{
		ID:          id,
		Topology:    topo,
		Version:     version,
		States:      states,
		Transitions: transitions,
		Initial:     states[0].ID,
	}
	if err := def.Normalize(); err != nil {
		return MachineDef{}, err
	}
	return def, nil
}

// FromSwarmRoles builds a TopologySwarm machine: orch -> workers -> merge.
func FromSwarmRoles(id, version string, roles []string) (MachineDef, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return MachineDef{}, fmt.Errorf("swarm id required")
	}
	if len(roles) == 0 {
		roles = []string{"plan", "exec"}
	}
	if len(roles) > 4 {
		roles = roles[:4]
	}
	states := []State{{ID: "orch", Kind: "orchestrator", Label: "orchestrator"}}
	var transitions []Transition
	for i, role := range roles {
		role = strings.TrimSpace(strings.ToLower(role))
		if role == "" {
			role = fmt.Sprintf("worker-%d", i)
		}
		sid := StateID("worker-" + role)
		states = append(states, State{ID: sid, Kind: "worker", Label: role, Meta: map[string]string{"role": role}})
		transitions = append(transitions, Transition{
			From: "orch", To: sid, Kind: EdgeParallel,
			Guard: GuardSpec{Kind: GuardAlways},
		})
	}
	states = append(states, State{ID: "merge", Kind: "merge", Label: "merge"})
	for _, role := range roles {
		role = strings.TrimSpace(strings.ToLower(role))
		if role == "" {
			continue
		}
		sid := StateID("worker-" + role)
		transitions = append(transitions, Transition{
			From: sid, To: "merge", Kind: EdgeMerge,
			Guard: GuardSpec{Kind: GuardAlways},
		})
	}
	def := MachineDef{
		ID:          id,
		Topology:    TopologySwarm,
		Version:     version,
		States:      states,
		Transitions: transitions,
		Initial:     "orch",
	}
	if err := def.Normalize(); err != nil {
		return MachineDef{}, err
	}
	return def, nil
}

// RelevancyFromSignals blends critic score, router, and optional contextgraph hint.
// This is the AI-first scoring input when an LLM router is not consulted mid-edge.
func RelevancyFromSignals(evalScore float64, evalPass bool, routerSignal string, graphHint float64) float64 {
	r := 0.5
	if graphHint > 0 {
		r = graphHint
	}
	// Blend critic signal.
	if evalScore > 0 {
		r = 0.4*r + 0.6*evalScore
	}
	if evalPass {
		r = r*0.7 + 0.3
	}
	switch strings.ToLower(routerSignal) {
	case "local":
		r += 0.02
	case "cloud":
		r += 0.01
	}
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r
}
