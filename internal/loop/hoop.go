package loop

import (
	"math"
	"strings"
	"time"
)

// HoopLearningConfig gates self-learning weight adjust (hoop learning MVP).
// Eval scores from the Critic stage feed LocalBias when route is auto.
// Named "hoop" so it is not confused with Cursor IDE /loop scheduling.
type HoopLearningConfig struct {
	Enabled       bool    `json:"enabled" yaml:"enabled"`
	LocalBiasStep float64 `json:"local_bias_step,omitempty" yaml:"local_bias_step,omitempty"` // default 0.05
	MaxBias       float64 `json:"max_bias,omitempty" yaml:"max_bias,omitempty"`               // default 0.5
	Window        int     `json:"window,omitempty" yaml:"window,omitempty"`                   // last N outcomes, default 20
}

// Defaults fills zero values.
func (c *HoopLearningConfig) Defaults() {
	if c.LocalBiasStep <= 0 {
		c.LocalBiasStep = 0.05
	}
	if c.MaxBias <= 0 {
		c.MaxBias = 0.5
	}
	if c.Window <= 0 {
		c.Window = 20
	}
}

// HoopState is persisted bias derived from iteration outcomes.
type HoopState struct {
	// LocalBias in [-MaxBias, +MaxBias]: positive → prefer local on auto route.
	LocalBias float64 `json:"local_bias"`
	// StagePrefs maps stage kind → preference score (higher = keep enabled / prefer).
	StagePrefs map[string]float64 `json:"stage_prefs,omitempty"`
	// PreferredStages is the top kinds by StagePrefs (for dashboard chips).
	PreferredStages []string  `json:"preferred_stages,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	Note            string    `json:"note,omitempty"`
}

// ApplyHoopLearning updates LocalBias and stage preferences from recent outcomes when enabled.
// Success on local raises bias; failure on local lowers it; cloud success
// slightly lowers local bias. Config-gated — no-op when disabled.
func ApplyHoopLearning(st *LoopState, cfg HoopLearningConfig) {
	if st == nil || !cfg.Enabled {
		return
	}
	cfg.Defaults()
	outcomes := st.Outcomes
	if len(outcomes) == 0 {
		return
	}
	if n := cfg.Window; n > 0 && len(outcomes) > n {
		outcomes = outcomes[len(outcomes)-n:]
	}

	var localOK, localFail, cloudOK, cloudFail int
	for _, o := range outcomes {
		route := strings.ToLower(o.Route)
		switch {
		case route == "local" && o.Success:
			localOK++
		case route == "local" && !o.Success:
			localFail++
		case route == "cloud" && o.Success:
			cloudOK++
		case route == "cloud" && !o.Success:
			cloudFail++
		}
	}
	delta := 0.0
	delta += float64(localOK) * cfg.LocalBiasStep
	delta -= float64(localFail) * cfg.LocalBiasStep
	delta -= float64(cloudOK) * (cfg.LocalBiasStep * 0.5)
	delta += float64(cloudFail) * (cfg.LocalBiasStep * 0.25)
	// Critic eval scores nudge bias: high local scores reinforce local.
	for _, o := range outcomes {
		if o.EvalScore <= 0 {
			continue
		}
		if strings.ToLower(o.Route) == "local" {
			delta += (o.EvalScore - 0.5) * cfg.LocalBiasStep
		}
	}

	prefs := map[string]float64{}
	for _, o := range outcomes {
		for _, s := range o.Stages {
			kind := strings.ToLower(strings.TrimSpace(s.Kind))
			if kind == "" {
				continue
			}
			if s.Success {
				prefs[kind] += cfg.LocalBiasStep
			} else {
				prefs[kind] -= cfg.LocalBiasStep * 0.5
			}
		}
	}
	top := rankStagePrefs(prefs, 3)

	bias := clamp(delta, -cfg.MaxBias, cfg.MaxBias)
	st.Hoop = HoopState{
		LocalBias:       bias,
		StagePrefs:      prefs,
		PreferredStages: top,
		UpdatedAt:       time.Now().UTC(),
		Note:            "hoop learning: outcome + eval_score → local bias; stage ok/fail → stage_prefs",
	}
}

func rankStagePrefs(prefs map[string]float64, n int) []string {
	if len(prefs) == 0 || n <= 0 {
		return nil
	}
	type kv struct {
		k string
		v float64
	}
	var list []kv
	for k, v := range prefs {
		if v <= 0 {
			continue
		}
		list = append(list, kv{k, v})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].v > list[i].v {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.k
	}
	return out
}

// EffectiveRoute returns the route for the next tick after hoop bias (when auto).
func EffectiveRoute(spec LoopSpec, hoop HoopState, cfg HoopLearningConfig) RoutePref {
	route := spec.Route
	if route == "" {
		route = RouteLocal
	}
	if route != RouteAuto || !cfg.Enabled {
		return route
	}
	cfg.Defaults()
	if hoop.LocalBias >= cfg.LocalBiasStep {
		return RouteLocal
	}
	if hoop.LocalBias <= -cfg.LocalBiasStep {
		return RouteCloud
	}
	return RouteAuto
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
