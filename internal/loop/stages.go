package loop

import (
	"fmt"
	"strings"
)

// StageKind is one swappable role in a Loop Engineering cycle
// (observe/plan/act/evaluate/learn). See planning/loop_engineering.md.
type StageKind string

const (
	StagePlanner   StageKind = "planner"    // discover / triage / decompose
	StageActor     StageKind = "actor"      // implement / produce artifact
	StageCritic    StageKind = "critic"     // maker/checker — emit eval score
	StageMemory    StageKind = "memory"     // load/persist durable state (no LLM by default)
	StageContext   StageKind = "context"    // early context hydrate (no LLM by default)
	StageWorkspace StageKind = "workspace"  // bind run or existing work/out roots
	StageRouter    StageKind = "router"     // which model/tools for following stages
	StageHumanGate StageKind = "human_gate" // HITL pause node (first-class)
)

// WorkspaceMode selects how a workspace stage binds tool paths.
type WorkspaceMode string

const (
	WorkspaceModeRun      WorkspaceMode = "run"      // ensure runs/<id>/{work,out}
	WorkspaceModeExisting WorkspaceMode = "existing" // reuse path under tools workspace
)

// AutonomyLevel matches cobusgreyling L1–L3 rollout.
type AutonomyLevel string

const (
	AutonomyL1 AutonomyLevel = "L1" // report / score only
	AutonomyL2 AutonomyLevel = "L2" // assisted
	AutonomyL3 AutonomyLevel = "L3" // unattended (gated)
)

// StageSpec is a hot-swappable stage module (prompt/skill/route).
// Alias: ModuleSpec in docs — same type.
type StageSpec struct {
	ID      string    `json:"id,omitempty" yaml:"id,omitempty"`
	Kind    StageKind `json:"kind" yaml:"kind"`
	Name    string    `json:"name,omitempty" yaml:"name,omitempty"`
	Prompt  string    `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Skill   string    `json:"skill,omitempty" yaml:"skill,omitempty"`
	Route   RoutePref `json:"route,omitempty" yaml:"route,omitempty"`
	Model   string    `json:"model,omitempty" yaml:"model,omitempty"`
	// Disabled skips this stage (hot-swap off). Default enabled.
	Disabled bool `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	// Enabled is accepted from the dashboard compose UI (false → Disabled).
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// EvalMin is the critic pass threshold (0–1). Only used for StageCritic.
	EvalMin float64 `json:"eval_min,omitempty" yaml:"eval_min,omitempty"`
	// Parallel > 1 fans out this stage (typically actor) via swarm.FanOut (max 4).
	Parallel int `json:"parallel,omitempty" yaml:"parallel,omitempty"`
	// Roles tags parallel workers (plan|exec|research|worker). Empty → worker-0..N.
	Roles []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	// Tools declares builtin / MCP / plugin capabilities on this node (internal/tools).
	Tools []ToolRef `json:"tools,omitempty" yaml:"tools,omitempty"`
	// WorkspaceMode: run (default) | existing — only for kind=workspace.
	WorkspaceMode WorkspaceMode `json:"workspace_mode,omitempty" yaml:"workspace_mode,omitempty"`
	// WorkspacePath is required when WorkspaceMode=existing (workspace-relative or abs under sandbox).
	WorkspacePath string `json:"workspace_path,omitempty" yaml:"workspace_path,omitempty"`
	// OutPath optionally overrides the out dir when mode=existing (else <workspace_path>/out).
	OutPath string `json:"out_path,omitempty" yaml:"out_path,omitempty"`
}

// ToolRef is a node-declared MCP server or local plugin capability.
type ToolRef struct {
	Name   string `json:"name" yaml:"name"`
	Kind   string `json:"kind,omitempty" yaml:"kind,omitempty"`     // mcp|plugin|builtin
	Server string `json:"server,omitempty" yaml:"server,omitempty"` // MCP server id
	Plugin string `json:"plugin,omitempty" yaml:"plugin,omitempty"` // local plugin id
}

// GraphEdge is a canvas edge persisted with the hoop so topology survives reload.
type GraphEdge struct {
	ID     string `json:"id,omitempty" yaml:"id,omitempty"`
	Source string `json:"source" yaml:"source"`
	Target string `json:"target" yaml:"target"`
	// Kind: flow|feedback|on_fail|escalate|conditional|budget_exceeded|parallel|merge.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`
	// Guard optionally overrides the default guard for this edge kind.
	Guard     string  `json:"guard,omitempty" yaml:"guard,omitempty"` // always|score_below|relevancy|...
	Threshold float64 `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Label     string  `json:"label,omitempty" yaml:"label,omitempty"`
}

// ModuleSpec is an alias for StageSpec (compose UI / hot-swap wording).
type ModuleSpec = StageSpec

// EvalSpec is critic/goal feedback configuration (maker ≠ checker).
type EvalSpec struct {
	Goal       string   `json:"goal,omitempty" yaml:"goal,omitempty"`
	OnSuccessN int      `json:"on_success_n,omitempty" yaml:"on_success_n,omitempty"`
	OnFailN    int      `json:"on_fail_n,omitempty" yaml:"on_fail_n,omitempty"`
	Contains   []string `json:"contains,omitempty" yaml:"contains,omitempty"`
	// MinScore overrides critic EvalMin when > 0.
	MinScore float64 `json:"min_score,omitempty" yaml:"min_score,omitempty"`
}

// IsEnabled reports whether the stage runs.
func (m StageSpec) IsEnabled() bool {
	return !m.Disabled
}

// Normalize fills defaults for one stage module.
func (m *StageSpec) Normalize() error {
	if m == nil {
		return fmt.Errorf("nil StageSpec")
	}
	m.Kind = StageKind(strings.ToLower(string(m.Kind)))
	switch m.Kind {
	case StagePlanner, StageActor, StageCritic, StageMemory, StageContext, StageWorkspace, StageRouter, StageHumanGate:
	default:
		return fmt.Errorf("unknown stage kind %q (planner|actor|critic|memory|context|workspace|router|human_gate)", m.Kind)
	}
	m.WorkspaceMode = WorkspaceMode(strings.ToLower(strings.TrimSpace(string(m.WorkspaceMode))))
	m.WorkspacePath = strings.TrimSpace(m.WorkspacePath)
	m.OutPath = strings.TrimSpace(m.OutPath)
	if m.Kind == StageWorkspace {
		switch m.WorkspaceMode {
		case "", WorkspaceModeRun:
			m.WorkspaceMode = WorkspaceModeRun
		case WorkspaceModeExisting:
			if m.WorkspacePath == "" {
				return fmt.Errorf("module %s: workspace_path required when workspace_mode=existing", m.ID)
			}
		default:
			return fmt.Errorf("module %s: workspace_mode must be run|existing", m.ID)
		}
	}
	for i := range m.Tools {
		m.Tools[i].Name = strings.TrimSpace(m.Tools[i].Name)
		m.Tools[i].Kind = strings.ToLower(strings.TrimSpace(m.Tools[i].Kind))
		if m.Tools[i].Kind == "" {
			m.Tools[i].Kind = "builtin"
		}
		m.Tools[i].Server = strings.TrimSpace(m.Tools[i].Server)
		m.Tools[i].Plugin = strings.TrimSpace(m.Tools[i].Plugin)
		if m.Tools[i].Name == "" {
			return fmt.Errorf("module %s: tools[%d]: name required", m.ID, i)
		}
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = string(m.Kind)
	}
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		m.ID = m.Name
	}
	m.Prompt = strings.TrimSpace(m.Prompt)
	m.Skill = strings.TrimSpace(m.Skill)
	m.Model = strings.TrimSpace(m.Model)
	m.Route = RoutePref(strings.ToLower(string(m.Route)))
	switch m.Route {
	case "", RouteLocal, RouteCloud, RouteAuto:
	default:
		return fmt.Errorf("module %s: route must be local|cloud|auto", m.ID)
	}
	if m.Enabled != nil && !*m.Enabled {
		m.Disabled = true
	}
	if m.Kind == StageCritic && m.EvalMin <= 0 {
		m.EvalMin = 0.7
	}
	if m.Parallel < 0 {
		return fmt.Errorf("module %s: parallel must be >= 0", m.ID)
	}
	if m.Parallel > 4 {
		m.Parallel = 4
	}
	for i := range m.Roles {
		m.Roles[i] = strings.TrimSpace(strings.ToLower(m.Roles[i]))
	}
	if m.Prompt == "" {
		switch m.Kind {
		case StagePlanner:
			m.Prompt = "You are the planner. Produce a short plan (bullet steps) for the next iteration. Do not implement yet."
		case StageActor:
			m.Prompt = "You are the implementer (maker). Follow the PLAN. Produce the concrete result for this iteration."
		case StageCritic:
			m.Prompt = defaultCriticPrompt
		}
	}
	return nil
}

const defaultCriticPrompt = `You are the checker (not the maker). Score whether the GOAL is satisfied by the ACTOR output.
Reply with exactly two lines:
SCORE: <number 0.0 to 1.0>
REASON: <one short sentence>`

// DefaultStages / DefaultModules — minimal maker/checker pipeline.
func DefaultStages() []StageSpec {
	return DefaultModules("complete the assigned task")
}

// DefaultModules returns a minimal maker/checker pipeline for a goal.
func DefaultModules(goal string) []StageSpec {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		goal = "complete the assigned task"
	}
	return []StageSpec{
		{Kind: StageWorkspace, Name: "workspace", ID: "workspace", WorkspaceMode: WorkspaceModeRun},
		{Kind: StageMemory, Name: "memory_load", ID: "memory_load"},
		{Kind: StageRouter, Name: "router", ID: "router"},
		{
			Kind:   StagePlanner,
			Name:   "planner",
			ID:     "planner",
			Prompt: "You are the planner. Goal:\n" + goal + "\n\nRead the prior state. Produce a short plan (bullet steps) for the next iteration. Do not implement yet.",
		},
		{
			Kind:   StageActor,
			Name:   "actor",
			ID:     "actor",
			Prompt: "You are the implementer (maker). Goal:\n" + goal + "\n\nFollow the PLAN below. Produce the concrete result for this iteration.",
		},
		{
			Kind:    StageCritic,
			Name:    "critic",
			ID:      "critic",
			Prompt:  defaultCriticPrompt,
			EvalMin: 0.7,
		},
		{Kind: StageMemory, Name: "memory_persist", ID: "memory_persist"},
	}
}

// NormalizeStages validates and fills defaults; empty → DefaultStages().
func NormalizeStages(in []StageSpec) ([]StageSpec, error) {
	if len(in) == 0 {
		out := DefaultStages()
		for i := range out {
			if err := out[i].Normalize(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	out := make([]StageSpec, 0, len(in))
	for i := range in {
		s := in[i]
		if err := s.Normalize(); err != nil {
			return nil, fmt.Errorf("stages[%d]: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// EnabledStages filters disabled modules.
func EnabledStages(in []StageSpec) []StageSpec {
	var out []StageSpec
	for _, s := range in {
		if s.IsEnabled() {
			out = append(out, s)
		}
	}
	return out
}

// ModuleCatalog is exposed on GET /api/loops/modules for the compose UI.
type ModuleCatalog struct {
	Kinds    []StageKind  `json:"kinds"`
	Defaults []StageSpec  `json:"defaults"`
	Notes    string       `json:"notes"`
}

// Catalog returns compose-UI metadata (pure data; no I/O).
func Catalog() ModuleCatalog {
	return ModuleCatalog{
		Kinds:    []StageKind{StageWorkspace, StagePlanner, StageActor, StageCritic, StageMemory, StageContext, StageRouter, StageHumanGate},
		Defaults: DefaultModules("your recursive goal"),
		Notes: "Loop Engineering hoop: compose Workspace/Planner/Actor/Critic (+ Memory/Context/Router/HumanGate). " +
			"Workspace binds runs/<id>/{work,out} or an existing path under ~/.glider/workspace. " +
			"Interval/cron is optional Automations heartbeat — not the definition of the loop. " +
			"Runtime is an AI-first state machine (graph|tree|loop|swarm). " +
			"See planning/loop_engineering.md and internal/statemachine.",
	}
}

// StagePrompt builds a stage user message for tests and tooling.
func StagePrompt(goal string, stage StageSpec, prior string, eval EvalSpec) string {
	var b strings.Builder
	b.WriteString("[stage:")
	b.WriteString(string(stage.Kind))
	b.WriteString("]\n")
	if stage.Prompt != "" {
		b.WriteString(stage.Prompt)
		b.WriteString("\n")
	}
	b.WriteString("GOAL:\n")
	b.WriteString(goal)
	if eval.Goal != "" {
		b.WriteString("\nEval: ")
		b.WriteString(eval.Goal)
	}
	if prior != "" {
		b.WriteString("\nPRIOR:\n")
		b.WriteString(prior)
	}
	return b.String()
}
