// Package loop implements Glider-owned Loop Engineering hoops (dashboard + API).
// Framing: design systems that prompt agents (planner/actor/critic + memory/router),
// not Cursor IDE /loop cron. Pure-local route needs only Ollama/vLLM.
// See planning/loop_engineering.md and https://addyosmani.com/blog/loop-engineering/
package loop

import (
	"fmt"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/contextkit"
)

// RoutePref selects how each iteration hits the shared harness.
type RoutePref string

const (
	// RouteLocal forces /local via Complete (gateway). Default for pure-local harness.
	RouteLocal RoutePref = "local"
	// RouteCloud forces /cloud via Complete (BYOK cloud keys; not Cursor sub).
	RouteCloud RoutePref = "cloud"
	// RouteAuto leaves routing to classifier/Starlark/ceiling.
	RouteAuto RoutePref = "auto"
)

// FailPolicy controls what happens when an iteration errors.
type FailPolicy string

const (
	FailStop      FailPolicy = "stop"      // halt the loop (default for local)
	FailContinue  FailPolicy = "continue"  // record failure, keep ticking
	FailEscalate  FailPolicy = "escalate"  // on local fail, one cloud retry then continue/stop
)

// Status is the persisted loop lifecycle state.
type Status string

const (
	StatusIdle      Status = "idle"
	StatusRunning   Status = "running"
	StatusStopped   Status = "stopped"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// StopConditions end the loop early when matched after an iteration.
type StopConditions struct {
	// OnSuccessN stops after N consecutive successes (0 = disabled).
	OnSuccessN int `json:"on_success_n,omitempty" yaml:"on_success_n,omitempty"`
	// OnFailN stops after N consecutive failures (0 = disabled).
	OnFailN int `json:"on_fail_n,omitempty" yaml:"on_fail_n,omitempty"`
	// Contains stops when the assistant text contains any substring (case-insensitive).
	Contains []string `json:"contains,omitempty" yaml:"contains,omitempty"`
	// MaxLatencyMS treats slower iterations as failures for OnFailN (0 = ignore).
	MaxLatencyMS int `json:"max_latency_ms,omitempty" yaml:"max_latency_ms,omitempty"`
}

// LoopSpec is the durable definition of a Loop Engineering hoop.
type LoopSpec struct {
	ID            string         `json:"id"`
	Name          string         `json:"name,omitempty"`
	// Goal is the recursive purpose (preferred). Prompt is kept as alias/fallback.
	Goal          string         `json:"goal,omitempty" yaml:"goal,omitempty"`
	Interval      string         `json:"interval,omitempty"` // optional Automations heartbeat
	Cron          string         `json:"cron,omitempty"`     // optional cadence (@every / 5-field)
	Prompt        string         `json:"prompt"`             // hoop goal / purpose (legacy field)
	Skill         string         `json:"skill,omitempty"`    // project knowledge hint
	MaxIterations int            `json:"max_iterations,omitempty"`
	Stop          StopConditions `json:"stop_conditions,omitempty" yaml:"stop_conditions,omitempty"`
	Route         RoutePref      `json:"route,omitempty"` // default local
	FailPolicy    FailPolicy     `json:"fail_policy,omitempty"`
	Model         string         `json:"model,omitempty"`
	// Autonomy is L1|L2|L3 (default L1 report/score).
	Autonomy AutonomyLevel `json:"autonomy,omitempty" yaml:"autonomy,omitempty"`
	// HumanGate forces escalation when critic fails (also implied by L1).
	HumanGate bool `json:"human_gate,omitempty" yaml:"human_gate,omitempty"`
	// Stages composes Loop Engineering stages (planner/actor/critic/memory/router).
	Stages []StageSpec `json:"stages,omitempty" yaml:"stages,omitempty"`
	// Eval is critic/goal feedback (maker ≠ checker).
	Eval EvalSpec `json:"eval,omitempty" yaml:"eval,omitempty"`
	// Learning enables hoop self-learning bias for this loop (overrides process default when true).
	Learning  bool      `json:"learning,omitempty" yaml:"learning,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IterationOutcome is one engineering-cycle result (hoop-learning + eval feedback).
type IterationOutcome struct {
	Iteration     int       `json:"iteration"`
	Success       bool      `json:"success"`
	LatencyMS     int64     `json:"latency_ms"`
	Route         string    `json:"route"` // local | cloud | auto
	TokenEstimate int       `json:"token_estimate,omitempty"`
	EpisodeID     string    `json:"episode_id,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	Err           string    `json:"err,omitempty"`
	EvalScore     float64   `json:"eval_score,omitempty"`
	// Stages records per-stage summaries for dashboard eval feedback.
	Stages []StageOutcome `json:"stages,omitempty"`
	At     time.Time      `json:"at"`
}

// StageOutcome is one stage result inside an iteration.
type StageOutcome struct {
	Kind    string `json:"kind"`
	Success bool   `json:"success"`
	Summary string `json:"summary,omitempty"`
	Err     string `json:"err,omitempty"`
	Model   string `json:"model,omitempty"`
}

// LoopState is persisted under ~/.glider/loops/<id>.json.
type LoopState struct {
	Spec          LoopSpec                  `json:"spec"`
	Status        Status                    `json:"status"`
	Iteration     int                       `json:"iteration"`
	Checkpoint    contextkit.LoopCheckpoint `json:"checkpoint"`
	Outcomes      []IterationOutcome        `json:"outcomes,omitempty"`
	Hoop          HoopState                 `json:"hoop,omitempty"`
	ConsecutiveOK int                       `json:"consecutive_ok,omitempty"`
	ConsecutiveFail int                     `json:"consecutive_fail,omitempty"`
	LastError     string                    `json:"last_error,omitempty"`
	LastEvalScore float64                   `json:"last_eval_score,omitempty"`
	StartedAt     *time.Time                `json:"started_at,omitempty"`
	StoppedAt     *time.Time                `json:"stopped_at,omitempty"`
	UpdatedAt     time.Time                 `json:"updated_at"`
}

// Normalize fills defaults on a create/update payload.
func (s *LoopSpec) Normalize() error {
	if s == nil {
		return fmt.Errorf("nil LoopSpec")
	}
	s.ID = strings.TrimSpace(s.ID)
	s.Name = strings.TrimSpace(s.Name)
	s.Goal = strings.TrimSpace(s.Goal)
	s.Prompt = strings.TrimSpace(s.Prompt)
	if s.Goal == "" {
		s.Goal = s.Prompt
	}
	if s.Prompt == "" {
		s.Prompt = s.Goal
	}
	s.Skill = strings.TrimSpace(s.Skill)
	s.Interval = strings.TrimSpace(s.Interval)
	s.Cron = strings.TrimSpace(s.Cron)
	s.Model = strings.TrimSpace(s.Model)
	s.Route = RoutePref(strings.ToLower(string(s.Route)))
	s.FailPolicy = FailPolicy(strings.ToLower(string(s.FailPolicy)))
	s.Autonomy = AutonomyLevel(strings.ToUpper(strings.TrimSpace(string(s.Autonomy))))

	if s.Prompt == "" && s.Skill == "" && s.Goal == "" {
		return fmt.Errorf("goal, prompt, or skill is required")
	}
	// Automations heartbeat is optional. Empty interval+cron → run cycles back-to-back
	// until stop (goal/eval/max). A tiny default delay avoids hot-spin when neither set
	// and max_iterations is unbounded — callers should set max_iterations for safety.
	if s.Interval != "" {
		if _, err := time.ParseDuration(s.Interval); err != nil {
			return fmt.Errorf("interval: %w", err)
		}
	}
	if s.Cron != "" {
		if _, err := parseSchedule(s.Cron, s.Interval); err != nil {
			return fmt.Errorf("cron: %w", err)
		}
	}
	switch s.Route {
	case "", RouteLocal:
		s.Route = RouteLocal
	case RouteCloud, RouteAuto:
	default:
		return fmt.Errorf("route must be local|cloud|auto")
	}
	switch s.FailPolicy {
	case "":
		if s.Route == RouteLocal {
			s.FailPolicy = FailStop
		} else {
			s.FailPolicy = FailContinue
		}
	case FailStop, FailContinue, FailEscalate:
	default:
		return fmt.Errorf("fail_policy must be stop|continue|escalate")
	}
	switch s.Autonomy {
	case "", AutonomyL1:
		s.Autonomy = AutonomyL1
	case AutonomyL2, AutonomyL3:
	default:
		return fmt.Errorf("autonomy must be L1|L2|L3")
	}
	if s.MaxIterations < 0 {
		return fmt.Errorf("max_iterations must be >= 0")
	}
	if len(s.Stages) == 0 {
		s.Stages = DefaultModules(s.Goal)
		for i := range s.Stages {
			if err := s.Stages[i].Normalize(); err != nil {
				return err
			}
		}
	} else {
		stages, err := NormalizeStages(s.Stages)
		if err != nil {
			return err
		}
		s.Stages = stages
	}
	s.Eval.Goal = strings.TrimSpace(s.Eval.Goal)
	if s.Eval.Goal == "" {
		s.Eval.Goal = s.Goal
	}
	// Merge eval into stop conditions when not already set.
	if s.Eval.OnSuccessN > 0 && s.Stop.OnSuccessN == 0 {
		s.Stop.OnSuccessN = s.Eval.OnSuccessN
	}
	if s.Eval.OnFailN > 0 && s.Stop.OnFailN == 0 {
		s.Stop.OnFailN = s.Eval.OnFailN
	}
	if len(s.Eval.Contains) > 0 && len(s.Stop.Contains) == 0 {
		s.Stop.Contains = append([]string(nil), s.Eval.Contains...)
	}
	if s.Eval.MinScore > 0 {
		for i := range s.Stages {
			if s.Stages[i].Kind == StageCritic && s.Stages[i].EvalMin <= 0 {
				s.Stages[i].EvalMin = s.Eval.MinScore
			}
		}
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	return nil
}

// EffectivePrompt builds the user message for one iteration (skill + goal + episode hint).
func (s LoopSpec) EffectivePrompt(cp contextkit.LoopCheckpoint) string {
	var b strings.Builder
	if s.Skill != "" {
		b.WriteString("[skill: ")
		b.WriteString(s.Skill)
		b.WriteString("]\n")
	}
	prompt := s.Prompt
	if prompt == "" {
		prompt = "Run skill " + s.Skill
	}
	b.WriteString(prompt)
	if cp.LastEpisodeID != "" || cp.EvalStatus != "" {
		b.WriteString("\n\n[loop_checkpoint episode=")
		b.WriteString(cp.LastEpisodeID)
		b.WriteString(" status=")
		b.WriteString(cp.EvalStatus)
		b.WriteString(" wake=")
		b.WriteString(cp.WakeReason)
		b.WriteString("]")
	}
	return b.String()
}
