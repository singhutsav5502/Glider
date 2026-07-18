package loop

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/google/uuid"
)

var scoreLine = regexp.MustCompile(`(?i)SCORE:\s*([0-9]*\.?[0-9]+)`)

// StageResult is one stage inside an engineering cycle.
type StageResult struct {
	Kind      StageKind `json:"kind"`
	ModuleID  string    `json:"module_id,omitempty"`
	Route     string    `json:"route,omitempty"`
	Text      string    `json:"text,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Tokens    int       `json:"tokens,omitempty"`
	EvalScore float64   `json:"eval_score,omitempty"`
	Err       string    `json:"err,omitempty"`
}

// CycleResult is one full observe→plan→act→evaluate→learn iteration.
type CycleResult struct {
	Iteration  int           `json:"iteration"`
	Stages     []StageResult `json:"stages"`
	EvalScore  float64       `json:"eval_score"`
	EvalPass   bool          `json:"eval_pass"`
	Success    bool          `json:"success"`
	Route      string        `json:"route"`
	LatencyMS  int64         `json:"latency_ms"`
	Tokens     int           `json:"token_estimate"`
	EpisodeID  string        `json:"episode_id,omitempty"`
	Summary    string        `json:"summary,omitempty"`
	HumanGate  bool          `json:"human_gate,omitempty"`
	Err        string        `json:"err,omitempty"`
	At         time.Time     `json:"at"`
}

// runCycle executes one Loop Engineering cycle for the hoop.
func (m *Manager) runCycle(ctx context.Context, st *LoopState) (CycleResult, string, error) {
	st.Iteration++
	iter := st.Iteration
	turnID := "loop:" + st.Spec.ID
	start := time.Now()
	goal := st.Spec.Goal
	if goal == "" {
		goal = st.Spec.Prompt
	}

	modules := EnabledStages(st.Spec.Stages)
	if len(modules) == 0 {
		modules = EnabledStages(DefaultModules(goal))
	}

	m.emit(st, contextgraph.EventLoopTick, map[string]string{
		"loop_id":   st.Spec.ID,
		"iteration": fmt.Sprintf("%d", iter),
		"kind":      "cycle",
	})

	var (
		planText   string
		actorText  string
		criticText string
		evalScore  float64
		evalPass   bool
		hadCritic  bool
		stages     []StageResult
		totalTok   int
		lastRoute  RoutePref
		cycleErr   error
	)

	defaultRoute := EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))
	lastRoute = defaultRoute

	for _, mod := range modules {
		if ctx.Err() != nil {
			cycleErr = ctx.Err()
			break
		}
		_ = mod.Normalize()
		if !mod.IsEnabled() {
			continue
		}
		switch mod.Kind {
		case StageMemory:
			// Memory is graph/state I/O — no LLM. Load is implicit via checkpoint; persist later.
			stages = append(stages, StageResult{Kind: StageMemory, ModuleID: mod.ID, Text: "ok"})
			continue
		case StageRouter:
			lastRoute = EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))
			if mod.Route != "" {
				lastRoute = mod.Route
			}
			stages = append(stages, StageResult{
				Kind: StageRouter, ModuleID: mod.ID, Route: string(lastRoute),
				Text: "route=" + string(lastRoute),
			})
			m.emit(st, contextgraph.EventRouteDecided, map[string]string{
				"loop_id": st.Spec.ID, "route": string(lastRoute), "stage": "router",
			})
			continue
		}

		route := lastRoute
		if mod.Route != "" {
			route = mod.Route
		}
		prompt := m.stagePrompt(st, mod, goal, planText, actorText)
		reqID := fmt.Sprintf("loop-%s-%d-%s", st.Spec.ID, iter, mod.ID)
		t0 := time.Now()
		text, tokens, used, err := m.completeOnce(ctx, st, route, reqID, turnID, prompt, mod.Model)
		sr := StageResult{
			Kind:      mod.Kind,
			ModuleID:  mod.ID,
			Route:     string(used),
			Text:      truncate(text, 2000),
			LatencyMS: time.Since(t0).Milliseconds(),
			Tokens:    tokens,
		}
		if err != nil {
			sr.Err = err.Error()
			stages = append(stages, sr)
			cycleErr = err
			// Escalate only for actor/planner local failures.
			if st.Spec.FailPolicy == FailEscalate && route == RouteLocal {
				text2, tok2, used2, err2 := m.completeOnce(ctx, st, RouteCloud, reqID+"-esc", turnID, prompt, mod.Model)
				if err2 == nil {
					sr.Text = truncate(text2, 2000)
					sr.Tokens = tok2
					sr.Route = string(used2)
					sr.Err = ""
					sr.LatencyMS = time.Since(t0).Milliseconds()
					cycleErr = nil
					text, tokens, used = text2, tok2, used2
					stages[len(stages)-1] = sr
				} else {
					break
				}
			} else {
				break
			}
		} else {
			stages = append(stages, sr)
		}
		totalTok += tokens
		lastRoute = used

		switch mod.Kind {
		case StagePlanner:
			planText = text
		case StageActor:
			actorText = text
		case StageCritic:
			hadCritic = true
			criticText = text
			evalScore = parseEvalScore(text)
			sr.EvalScore = evalScore
			stages[len(stages)-1] = sr
			min := mod.EvalMin
			if min <= 0 {
				min = 0.7
			}
			evalPass = evalScore >= min
		}
	}

	epID := uuid.New().String()
	summary := truncate(firstNonEmpty(criticText, actorText, planText), 512)
	lat := time.Since(start).Milliseconds()
	success := cycleErr == nil && (!hadCritic || evalPass)

	humanGate := false
	if hadCritic && !evalPass && (st.Spec.Autonomy == AutonomyL1 || st.Spec.HumanGate) {
		humanGate = true
	}

	cr := CycleResult{
		Iteration: iter,
		Stages:    stages,
		EvalScore: evalScore,
		EvalPass:  evalPass,
		Success:   success,
		Route:     string(lastRoute),
		LatencyMS: lat,
		Tokens:    totalTok,
		EpisodeID: epID,
		Summary:   summary,
		HumanGate: humanGate,
		At:        time.Now().UTC(),
	}
	if cycleErr != nil {
		cr.Err = cycleErr.Error()
	}

	// Persist episode + graph.
	if m.Episodes != nil {
		m.Episodes.RecordEpisode(turnID, contextkit.Episode{
			ID:        epID,
			Summary:   summary,
			Tokens:    totalTok,
			Model:     st.Spec.Model,
			Reason:    "loop_cycle",
			Role:      "critic",
			CreatedAt: time.Now().UTC(),
		})
		m.Episodes.SetLoop(turnID, contextkit.LoopCheckpoint{
			Goal:          goal,
			LastEpisodeID: epID,
			EvalStatus:    map[bool]string{true: "pass", false: "fail"}[success],
			WakeReason:    "cycle",
		})
	}
	attrs := map[string]string{
		"loop_id":    st.Spec.ID,
		"iteration":  fmt.Sprintf("%d", iter),
		"eval_score": fmt.Sprintf("%.3f", evalScore),
		"eval_pass":  fmt.Sprintf("%t", evalPass),
		"success":    fmt.Sprintf("%t", success),
		"episode_id": epID,
	}
	if success {
		m.emit(st, contextgraph.EventFulfilledLocal, attrs)
	} else {
		if cr.Err != "" {
			attrs["err"] = cr.Err
		}
		m.emit(st, contextgraph.EventError, attrs)
	}
	if g := m.Graph; g != nil {
		g.Append(contextgraph.Event{
			Kind:      contextgraph.EventEpisodeMerged,
			TurnID:    turnID,
			RequestID: fmt.Sprintf("loop-%s-%d", st.Spec.ID, iter),
			Actor:     "loop",
			Attrs:     attrs,
		})
	}

	outcome := IterationOutcome{
		Iteration:     iter,
		Success:       success,
		LatencyMS:     lat,
		Route:         string(lastRoute),
		TokenEstimate: totalTok,
		EpisodeID:     epID,
		Summary:       summary,
		EvalScore:     evalScore,
		At:            cr.At,
		Err:           cr.Err,
	}
	for _, sr := range stages {
		outcome.Stages = append(outcome.Stages, StageOutcome{
			Kind:    string(sr.Kind),
			Success: sr.Err == "",
			Summary: truncate(sr.Text, 160),
			Err:     sr.Err,
		})
	}
	st.AppendOutcome(outcome, m.Cfg.OutcomeRing)
	ApplyHoopLearning(st, m.learningCfg(st))
	st.Checkpoint.LastEpisodeID = epID
	if success {
		st.Checkpoint.EvalStatus = "pass"
	} else if humanGate {
		st.Checkpoint.EvalStatus = "human_gate"
	} else {
		st.Checkpoint.EvalStatus = "fail"
	}
	st.LastError = cr.Err
	st.LastEvalScore = evalScore

	stopReason := m.evaluateStop(st, outcome, summary)
	if stopReason == "" && hadCritic && evalPass && st.Spec.Stop.OnSuccessN <= 0 && st.Spec.MaxIterations <= 0 {
		// Goal-style: critic pass ends the recursive goal when no schedule max set.
		if st.Spec.Interval == "" && st.Spec.Cron == "" {
			stopReason = "eval_pass"
		}
	}
	if stopReason == "" && humanGate && st.Spec.Autonomy == AutonomyL1 {
		stopReason = "human_gate"
	}
	if stopReason == "" && !success {
		switch st.Spec.FailPolicy {
		case FailStop, "":
			stopReason = "failed"
		}
	}
	if stopReason == "" && st.Spec.MaxIterations > 0 && iter >= st.Spec.MaxIterations {
		stopReason = "max_iterations"
	}
	return cr, stopReason, cycleErr
}

func (m *Manager) learningCfg(st *LoopState) HoopLearningConfig {
	cfg := m.Cfg.Hoop
	if st != nil && st.Spec.Learning {
		cfg.Enabled = true
	}
	return cfg
}

func (m *Manager) stagePrompt(st *LoopState, mod ModuleSpec, goal, plan, actor string) string {
	var b strings.Builder
	if mod.Skill != "" {
		b.WriteString("[skill: ")
		b.WriteString(mod.Skill)
		b.WriteString("]\n")
	}
	prompt := mod.Prompt
	if prompt == "" {
		prompt = st.Spec.EffectivePrompt(st.Checkpoint)
	}
	b.WriteString(prompt)
	b.WriteString("\n\nGOAL:\n")
	b.WriteString(goal)
	if st.Checkpoint.LastEpisodeID != "" {
		b.WriteString("\n\nPRIOR_EPISODE: ")
		b.WriteString(st.Checkpoint.LastEpisodeID)
		b.WriteString(" status=")
		b.WriteString(st.Checkpoint.EvalStatus)
	}
	if plan != "" && mod.Kind != StagePlanner {
		b.WriteString("\n\nPLAN:\n")
		b.WriteString(truncate(plan, 3000))
	}
	if actor != "" && mod.Kind == StageCritic {
		b.WriteString("\n\nACTOR_OUTPUT:\n")
		b.WriteString(truncate(actor, 4000))
	}
	return b.String()
}

func parseEvalScore(text string) float64 {
	m := scoreLine.FindStringSubmatch(text)
	if len(m) < 2 {
		// Fallback: look for a lone 0.x / 1.0 near the start.
		fields := strings.Fields(text)
		for _, f := range fields {
			f = strings.Trim(f, ":,")
			if v, err := strconv.ParseFloat(f, 64); err == nil && v >= 0 && v <= 1 {
				return v
			}
		}
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// completeOnce forces route via /local|/cloud prefix and drains the harness stream.
func (m *Manager) completeOnce(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string) (text string, tokens int, used RoutePref, err error) {
	used = route
	switch route {
	case RouteLocal:
		prompt = "/local " + prompt
	case RouteCloud:
		prompt = "/cloud " + prompt
	}
	if model == "" {
		model = st.Spec.Model
	}
	req := &backend.CompletionRequest{
		Model:  model,
		Stream: true,
		Messages: []backend.Message{
			{Role: "user", Content: prompt},
		},
		Metadata: backend.RequestMetadata{RequestID: reqID},
	}
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://glider.loop/v1/chat/completions", nil)
	httpReq.Header.Set("X-Glider-Loop-ID", st.Spec.ID)
	httpReq.Header.Set("X-Glider-Turn-ID", turnID)
	httpReq.Header.Set("X-Glider-Loop-Kind", "engineering_cycle")

	ch, err := m.Complete.Complete(httpReq, req)
	if err != nil {
		return "", req.Metadata.EstimatedTokens, used, err
	}
	var b strings.Builder
	for chunk := range ch {
		b.WriteString(chunk.Content)
		if ctx.Err() != nil {
			return b.String(), req.Metadata.EstimatedTokens, used, ctx.Err()
		}
	}
	tokens = req.Metadata.EstimatedTokens
	if tokens == 0 {
		tokens = len(b.String()) / 4
	}
	return b.String(), tokens, used, nil
}
