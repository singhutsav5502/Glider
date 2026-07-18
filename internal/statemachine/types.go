// Package statemachine is Glider's AI-first, relevancy-driven orchestration core.
//
// One runtime model expresses four topology shapes:
//   - graph  — DAGs with merges and conditional edges
//   - tree   — hierarchical decompose / fan-out branches
//   - loop   — cyclic plan-act-critique with feedback edges
//   - swarm  — parallel workers under an orchestrator with fan-in
//
// Hoop and swarm UI graphs are views onto MachineDef. Transitions are chosen by
// Guards; when intelligence is needed, relevancy / router / critic scores drive
// the pick (heuristics and policy are fallbacks).
//
// AI-first decision points (see Engine.Next / ScoreTransition):
//  1. Outgoing edge choice among Guard-passers ranked by relevancy
//  2. Feedback vs escalate after critic fail
//  3. Swarm / tree parallel branches under a fan-out parent
//  4. HITL resume via GuardHumanApproved
//  5. Budget / policy edges when DecisionContext.BudgetOK is false
package statemachine

import (
	"fmt"
	"strings"
	"time"
)

// Topology is the high-level shape of a machine.
type Topology string

const (
	TopologyGraph Topology = "graph"
	TopologyTree  Topology = "tree"
	TopologyLoop  Topology = "loop"
	TopologySwarm Topology = "swarm"
)

// Status is runtime lifecycle for one machine instance.
type Status string

const (
	StatusIdle         Status = "idle"
	StatusRunning      Status = "running"
	StatusWaitingHuman Status = "waiting_human"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusPaused       Status = "paused"
)

// StateID identifies a node in the machine.
type StateID string

// State is one node (stage / worker / gate / merge).
type State struct {
	ID    StateID           `json:"id"`
	Kind  string            `json:"kind"`
	Label string            `json:"label,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
	Tools []ToolRef         `json:"tools,omitempty"`
}

// ToolRef is a declared capability on a node (MCP server or local plugin).
type ToolRef struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`   // mcp|plugin|builtin
	Server string `json:"server,omitempty"` // MCP server id
	Plugin string `json:"plugin,omitempty"` // local plugin id
}

// EdgeKind classifies a transition for guards and visualization.
type EdgeKind string

const (
	EdgeFlow           EdgeKind = "flow"
	EdgeFeedback       EdgeKind = "feedback"
	EdgeOnFail         EdgeKind = "on_fail"
	EdgeEscalate       EdgeKind = "escalate"
	EdgeConditional    EdgeKind = "conditional"
	EdgeBudgetExceeded EdgeKind = "budget_exceeded"
	EdgeParallel       EdgeKind = "parallel"
	EdgeMerge          EdgeKind = "merge"
)

// GuardKind selects how a transition is allowed.
type GuardKind string

const (
	GuardAlways         GuardKind = "always"
	GuardScoreBelow     GuardKind = "score_below"
	GuardScoreAbove     GuardKind = "score_above"
	GuardHeuristic      GuardKind = "heuristic"
	GuardRelevancy      GuardKind = "relevancy"
	GuardPolicy         GuardKind = "policy"
	GuardHumanApproved  GuardKind = "human_approved"
	GuardBudgetExceeded GuardKind = "budget_exceeded"
)

// GuardSpec is attached to a Transition.
type GuardSpec struct {
	Kind         GuardKind `json:"kind,omitempty"`
	Threshold    float64   `json:"threshold,omitempty"`
	MinRelevancy float64   `json:"min_relevancy,omitempty"`
	Expr         string    `json:"expr,omitempty"`
}

// Transition is a directed edge with an optional guard.
type Transition struct {
	ID       string    `json:"id,omitempty"`
	From     StateID   `json:"from"`
	To       StateID   `json:"to"`
	Kind     EdgeKind  `json:"kind,omitempty"`
	Guard    GuardSpec `json:"guard,omitempty"`
	Priority int       `json:"priority,omitempty"`
	Label    string    `json:"label,omitempty"`
}

// ActionKind is what happens on enter/exit of a state.
type ActionKind string

const (
	ActionEnter       ActionKind = "enter"
	ActionExit        ActionKind = "exit"
	ActionInvokeLLM   ActionKind = "invoke_llm"
	ActionFanOut      ActionKind = "fan_out"
	ActionMerge       ActionKind = "merge"
	ActionWaitHuman   ActionKind = "wait_human"
	ActionInvokeTools ActionKind = "invoke_tools"
)

// Action is a named hook bound to a state.
type Action struct {
	Kind ActionKind `json:"kind"`
	Name string     `json:"name,omitempty"`
}

// MachineDef is the durable topology (YAML/JSON view maps here).
type MachineDef struct {
	ID          string       `json:"id"`
	Topology    Topology     `json:"topology"`
	Version     string       `json:"version,omitempty"`
	States      []State      `json:"states"`
	Transitions []Transition `json:"transitions"`
	Initial     StateID      `json:"initial"`
}

// HumanDecision is the durable HITL payload.
type HumanDecision struct {
	Approved bool      `json:"approved"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	GateNode StateID   `json:"gate_node,omitempty"`
}

// DecisionContext feeds AI-first / heuristic guards.
type DecisionContext struct {
	EvalScore     float64           `json:"eval_score,omitempty"`
	EvalPass      bool              `json:"eval_pass,omitempty"`
	RouterSignal  string            `json:"router_signal,omitempty"`
	Relevancy     float64           `json:"relevancy,omitempty"`
	TaskHints     map[string]string `json:"task_hints,omitempty"`
	BudgetOK      bool              `json:"budget_ok"`
	Human         *HumanDecision    `json:"human,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
	FailedWorkers []string          `json:"failed_workers,omitempty"`
	Attrs         map[string]string `json:"attrs,omitempty"`
}

// BranchChoice is one candidate next edge for live visualization.
type BranchChoice struct {
	EdgeID   string  `json:"edge_id"`
	To       StateID `json:"to"`
	Kind     string  `json:"kind"`
	Score    float64 `json:"score"`
	Selected bool    `json:"selected,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// DecisionRoute is the live path state painted on Cytoscape.
type DecisionRoute struct {
	Current       StateID        `json:"current,omitempty"`
	PathTaken     []StateID      `json:"path_taken,omitempty"`
	EdgesTaken    []string       `json:"edges_taken,omitempty"`
	NextEdges     []string       `json:"next_edges,omitempty"`
	BranchChoices []BranchChoice `json:"branch_choices,omitempty"`
	Topology      Topology       `json:"topology,omitempty"`
	Status        Status         `json:"status,omitempty"`
	Note          string         `json:"note,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at,omitempty"`
}

// Runtime is one executing instance of a MachineDef.
type Runtime struct {
	Def          MachineDef      `json:"def"`
	Current      StateID         `json:"current"`
	Status       Status          `json:"status"`
	Path         []StateID       `json:"path,omitempty"`
	EdgesTaken   []string        `json:"edges_taken,omitempty"`
	Context      DecisionContext `json:"context"`
	Route        DecisionRoute   `json:"route"`
	WaitingAt    StateID         `json:"waiting_at,omitempty"`
	PendingHuman *HumanDecision  `json:"pending_human,omitempty"`
}

// ValidEdgeKind reports whether k is a known edge kind.
func ValidEdgeKind(k EdgeKind) bool {
	switch k {
	case EdgeFlow, EdgeFeedback, EdgeOnFail, EdgeEscalate, EdgeConditional,
		EdgeBudgetExceeded, EdgeParallel, EdgeMerge:
		return true
	default:
		return false
	}
}

// Normalize fills defaults on a MachineDef.
func (d *MachineDef) Normalize() error {
	if d == nil {
		return fmt.Errorf("nil MachineDef")
	}
	d.ID = strings.TrimSpace(d.ID)
	if d.ID == "" {
		return fmt.Errorf("machine id required")
	}
	d.Topology = Topology(strings.ToLower(string(d.Topology)))
	switch d.Topology {
	case "", TopologyLoop:
		d.Topology = TopologyLoop
	case TopologyGraph, TopologyTree, TopologySwarm:
	default:
		return fmt.Errorf("topology must be graph|tree|loop|swarm")
	}
	if len(d.States) == 0 {
		return fmt.Errorf("machine needs at least one state")
	}
	ids := map[StateID]bool{}
	for i := range d.States {
		s := &d.States[i]
		s.ID = StateID(strings.TrimSpace(string(s.ID)))
		s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
		s.Label = strings.TrimSpace(s.Label)
		if s.ID == "" {
			return fmt.Errorf("states[%d]: id required", i)
		}
		if s.Kind == "" {
			s.Kind = "stage"
		}
		if s.Label == "" {
			s.Label = string(s.ID)
		}
		if ids[s.ID] {
			return fmt.Errorf("duplicate state id %q", s.ID)
		}
		ids[s.ID] = true
	}
	if d.Initial == "" {
		d.Initial = d.States[0].ID
	}
	if !ids[d.Initial] {
		return fmt.Errorf("initial state %q not in states", d.Initial)
	}
	for i := range d.Transitions {
		t := &d.Transitions[i]
		t.From = StateID(strings.TrimSpace(string(t.From)))
		t.To = StateID(strings.TrimSpace(string(t.To)))
		t.Kind = EdgeKind(strings.ToLower(strings.TrimSpace(string(t.Kind))))
		if t.Kind == "" {
			t.Kind = EdgeFlow
		}
		if !ValidEdgeKind(t.Kind) {
			return fmt.Errorf("transitions[%d]: invalid kind %q", i, t.Kind)
		}
		if t.From == "" || t.To == "" {
			return fmt.Errorf("transitions[%d]: from and to required", i)
		}
		if !ids[t.From] || !ids[t.To] {
			return fmt.Errorf("transitions[%d]: unknown endpoint", i)
		}
		if t.ID == "" {
			t.ID = fmt.Sprintf("%s->%s:%s", t.From, t.To, t.Kind)
		}
		if t.Guard.Kind == "" {
			t.Guard.Kind = defaultGuardForEdge(t.Kind)
		}
	}
	if d.Version == "" {
		d.Version = "1"
	}
	return nil
}

func defaultGuardForEdge(k EdgeKind) GuardKind {
	switch k {
	case EdgeFeedback, EdgeOnFail, EdgeEscalate:
		return GuardScoreBelow
	case EdgeBudgetExceeded:
		return GuardBudgetExceeded
	case EdgeConditional:
		return GuardRelevancy
	default:
		return GuardAlways
	}
}

// StateByID returns a state or nil.
func (d MachineDef) StateByID(id StateID) *State {
	for i := range d.States {
		if d.States[i].ID == id {
			return &d.States[i]
		}
	}
	return nil
}

// Outgoing returns transitions from id.
func (d MachineDef) Outgoing(id StateID) []Transition {
	var out []Transition
	for _, t := range d.Transitions {
		if t.From == id {
			out = append(out, t)
		}
	}
	return out
}

// DetectTopology infers topology from edge kinds when not set.
func DetectTopology(transitions []Transition, hasParallel bool) Topology {
	hasFeedback := false
	hasParallelEdge := hasParallel
	hasMerge := false
	for _, t := range transitions {
		switch t.Kind {
		case EdgeFeedback:
			hasFeedback = true
		case EdgeParallel:
			hasParallelEdge = true
		case EdgeMerge:
			hasMerge = true
		}
	}
	switch {
	case hasParallelEdge || hasMerge:
		return TopologySwarm
	case hasFeedback:
		return TopologyLoop
	default:
		outCount := map[StateID]int{}
		for _, t := range transitions {
			if t.Kind == EdgeFlow || t.Kind == EdgeConditional {
				outCount[t.From]++
			}
		}
		for _, n := range outCount {
			if n > 1 {
				return TopologyTree
			}
		}
		return TopologyGraph
	}
}
