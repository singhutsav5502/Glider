package loop

import (
	"strings"
	"time"
)

// GovernanceSpec is per-hoop FinOps / safety budgets (MVP).
// Soft limits degrade (prefer local, skip optional tools); hard limits stop the run.
type GovernanceSpec struct {
	// SoftTokens: when spent >= soft, PreferLocal and skip non-required tools.
	SoftTokens int `json:"soft_tokens,omitempty" yaml:"soft_tokens,omitempty"`
	// HardTokens: when spent >= hard, stop with budget_exceeded.
	HardTokens int `json:"hard_tokens,omitempty" yaml:"hard_tokens,omitempty"`
	// SoftLatencyMS / HardLatencyMS for cycle wall time.
	SoftLatencyMS int `json:"soft_latency_ms,omitempty" yaml:"soft_latency_ms,omitempty"`
	HardLatencyMS int `json:"hard_latency_ms,omitempty" yaml:"hard_latency_ms,omitempty"`
	// SoftCostUSD / HardCostUSD estimated spend (token*rate heuristic).
	SoftCostUSD float64 `json:"soft_cost_usd,omitempty" yaml:"soft_cost_usd,omitempty"`
	HardCostUSD float64 `json:"hard_cost_usd,omitempty" yaml:"hard_cost_usd,omitempty"`
	// USDPer1kTokens estimates cost (default 0.002).
	USDPer1kTokens float64 `json:"usd_per_1k_tokens,omitempty" yaml:"usd_per_1k_tokens,omitempty"`
	// MaxRPM soft rate limit across cycles (0 = off).
	MaxRPM int `json:"max_rpm,omitempty" yaml:"max_rpm,omitempty"`
	// ToolDenylist blocks named tools even if declared on a stage.
	ToolDenylist []string `json:"tool_denylist,omitempty" yaml:"tool_denylist,omitempty"`
	// PreferLocalOnSoft forces RouteLocal when soft budget exceeded.
	PreferLocalOnSoft bool `json:"prefer_local_on_soft,omitempty" yaml:"prefer_local_on_soft,omitempty"`
}

// BudgetSpend tracks cumulative spend for a hoop run (persisted on LoopState).
type BudgetSpend struct {
	Tokens    int       `json:"tokens,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	SoftHit   bool      `json:"soft_hit,omitempty"`
	HardHit   bool      `json:"hard_hit,omitempty"`
	LastCheck time.Time `json:"last_check,omitempty"`
	// CycleStarts are unix millis for RPM window.
	CycleStarts []int64 `json:"cycle_starts,omitempty"`
}

// MachineCursor persists mid-cycle / mid-machine position for HITL resume.
type MachineCursor struct {
	Active         bool     `json:"active,omitempty"`
	ResumeStageID  string   `json:"resume_stage_id,omitempty"`  // continue AFTER this stage
	ResumeIndex    int      `json:"resume_index,omitempty"`     // index in enabled walk order
	Iteration      int      `json:"iteration,omitempty"`        // cycle iteration to keep
	PlanText       string   `json:"plan_text,omitempty"`
	ActorText      string   `json:"actor_text,omitempty"`
	CriticText     string   `json:"critic_text,omitempty"`
	PathTaken      []string `json:"path_taken,omitempty"`
	EvalScore      float64  `json:"eval_score,omitempty"`
	EvalPass       bool     `json:"eval_pass,omitempty"`
	HadCritic      bool     `json:"had_critic,omitempty"`
	LastRoute      string   `json:"last_route,omitempty"`
	PartialSummary string   `json:"partial_summary,omitempty"`
}

func (g GovernanceSpec) rate() float64 {
	if g.USDPer1kTokens > 0 {
		return g.USDPer1kTokens
	}
	return 0.002
}

func (g GovernanceSpec) estimateCost(tokens int) float64 {
	return float64(tokens) / 1000.0 * g.rate()
}

func denylistHit(deny []string, name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, d := range deny {
		if strings.EqualFold(strings.TrimSpace(d), name) {
			return true
		}
	}
	return false
}
