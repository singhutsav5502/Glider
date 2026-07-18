package loop

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/statemachine"
	"github.com/glider-ai/glider/internal/swarm"
	"github.com/glider-ai/glider/internal/tools"
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

	modules, smRT, smErr := stageOrderFromMachine(st.Spec)
	if smErr != nil && m.Logs != nil {
		m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, "statemachine", "build: "+smErr.Error(), nil)
	}
	if len(modules) == 0 {
		modules = EnabledStages(DefaultModules(goal))
	}
	if smRT != nil {
		smRT.Enter()
		smRT.SetContext(statemachine.DecisionContext{
			BudgetOK:     true,
			RouterSignal: string(EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))),
			Relevancy:    relevancyHint(0, false, EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st)), 0),
		})
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
		waitHuman  bool
	)

	defaultRoute := EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))
	lastRoute = defaultRoute

	for stageIdx, mod := range modules {
		if ctx.Err() != nil {
			cycleErr = ctx.Err()
			break
		}
		_ = mod.Normalize()
		if !mod.IsEnabled() {
			continue
		}
		// Sync state machine current node + live DecisionRoute.
		if smRT != nil {
			smRT.Current = statemachine.StateID(mod.ID)
			if len(smRT.Path) == 0 || smRT.Path[len(smRT.Path)-1] != smRT.Current {
				smRT.Path = append(smRT.Path, smRT.Current)
			}
			smRT.Status = statemachine.StatusRunning
			smRT.SetContext(statemachine.DecisionContext{
				BudgetOK:     m.budgetOK(st),
				EvalScore:    evalScore,
				EvalPass:     evalPass,
				RouterSignal: string(lastRoute),
				Relevancy:    relevancyHint(evalScore, evalPass, lastRoute, m.graphEventCount(turnID)),
				LastError:    "",
				Human:        humanToSM(st),
			})
			_, _ = smRT.Next() // populate branch choices for viz
			m.emitRouteDecision(st, smRT, mod)
		}
		m.setProgress(st, stagePhase(mod.Kind), mod, stageIdx, iter, summarizeBranch(smRT))
		if smRT != nil {
			st.Progress = progressFromRoute(smRT, st.Progress)
			_ = m.Store.Save(st)
		}

		// HITL first-class node: pause cycle until approve/resume.
		if mod.Kind == StageHumanGate {
			openGate(st, mod, "human_gate stage")
			st.Progress = progressFromRoute(smRT, st.Progress)
			st.Progress.Phase = "waiting_human"
			_ = m.Store.Save(st)
			if m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "hitl", "paused at human_gate "+mod.ID, map[string]string{
					"stage_id": mod.ID,
				})
			}
			waitHuman = true
			stages = append(stages, StageResult{Kind: StageHumanGate, ModuleID: mod.ID, Text: "waiting_human"})
			break
		}

		// Node tools (MCP/plugin) — invoke before LLM stages when declared.
		if len(mod.Tools) > 0 && m.Tools != nil {
			refs := make([]tools.Ref, 0, len(mod.Tools))
			for _, t := range mod.Tools {
				refs = append(refs, tools.Ref{Name: t.Name, Kind: tools.Kind(t.Kind), Server: t.Server, Plugin: t.Plugin})
			}
			toolResults := m.Tools.InvokeAll(ctx, refs, goal)
			for _, tr := range toolResults {
				msg := tr.Name + " ok=" + fmt.Sprintf("%t", tr.OK)
				if tr.Stubbed {
					msg += " stub"
				}
				if m.Logs != nil {
					m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "tool", msg, map[string]string{
						"stage_id": mod.ID, "tool": tr.Name, "kind": string(tr.Kind),
					})
				}
				if m.Graph != nil {
					m.Graph.Append(contextgraph.Event{
						Kind:   contextgraph.EventToolFinished,
						TurnID: turnID,
						Actor:  "loop",
						Attrs: map[string]string{
							"tool": tr.Name, "ok": fmt.Sprintf("%t", tr.OK), "stage": mod.ID,
							"provenance": string(contextgraph.ProvenanceExtracted),
						},
					})
				}
			}
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

		var text string
		var tokens int
		var used RoutePref
		var err error

		// Parallel actor (or any LLM stage with Parallel>1) → swarm fan-out + critique merge.
		if mod.Parallel > 1 && (mod.Kind == StageActor || mod.Kind == StagePlanner) {
			text, tokens, used, err = m.completeParallel(ctx, st, mod, route, reqID, turnID, prompt)
		} else {
			text, tokens, used, err = m.completeOnce(ctx, st, route, reqID, turnID, prompt, mod.Model)
		}

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
			// Escalate only for actor/planner local failures (single-path; parallel already merged).
			if mod.Parallel <= 1 && st.Spec.FailPolicy == FailEscalate && route == RouteLocal {
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
		if m.Logs != nil {
			kind := "stage_end"
			msg := fmt.Sprintf("%s done route=%s %dms", mod.Kind, used, sr.LatencyMS)
			if sr.Err != "" {
				m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, kind, msg+" err="+truncate(sr.Err, 120), map[string]string{
					"stage": string(mod.Kind), "route": string(used),
				})
			} else {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, kind, msg, map[string]string{
					"stage": string(mod.Kind), "route": string(used),
				})
			}
		}

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
			if m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "eval", fmt.Sprintf("SCORE: %.3f pass=%t", evalScore, evalPass), map[string]string{
					"eval_score": fmt.Sprintf("%.3f", evalScore),
					"eval_pass":  fmt.Sprintf("%t", evalPass),
				})
			}
		}
	}
	m.setProgress(st, "learn", ModuleSpec{Kind: StageMemory, ID: "learn"}, len(modules), iter, "persisting")

	epID := uuid.New().String()
	summary := truncate(firstNonEmpty(criticText, actorText, planText), 512)
	lat := time.Since(start).Milliseconds()
	success := cycleErr == nil && (!hadCritic || evalPass)

	humanGate := false
	if hadCritic && !evalPass && (st.Spec.Autonomy == AutonomyL1 || st.Spec.HumanGate) {
		humanGate = true
	}
	// Critic-fail auto-gate: durable wait when configured (unless already paused mid-cycle).
	if !waitHuman && humanGate {
		gateMod := ModuleSpec{Kind: StageHumanGate, ID: "human_gate", Name: "human_gate"}
		for _, s := range st.Spec.Stages {
			if s.Kind == StageHumanGate {
				gateMod = s
				break
			}
		}
		openGate(st, gateMod, fmt.Sprintf("critic fail score=%.3f", evalScore))
		waitHuman = true
		if fb := pickFeedbackTarget(st.Spec, evalScore, evalPass); fb != "" && m.Logs != nil {
			m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "route_decision", "feedback candidate "+fb, map[string]string{
				"target": fb, "eval_score": fmt.Sprintf("%.3f", evalScore),
			})
		}
	}

	cr := CycleResult{
		Iteration: iter,
		Stages:    stages,
		EvalScore: evalScore,
		EvalPass:  evalPass,
		Success:   success && !waitHuman,
		Route:     string(lastRoute),
		LatencyMS: lat,
		Tokens:    totalTok,
		EpisodeID: epID,
		Summary:   summary,
		HumanGate: humanGate || waitHuman,
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
	// MaxLatencyMS treats slow cycles as failures for stop OnFailN counting.
	if sc := st.Spec.Stop.MaxLatencyMS; sc > 0 && lat > int64(sc) {
		outcome.Success = false
		cr.Success = false
		success = false
		if outcome.Err == "" {
			outcome.Err = fmt.Sprintf("max_latency_ms: %dms > %dms", lat, sc)
			cr.Err = outcome.Err
		}
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
	if waitHuman {
		st.Checkpoint.EvalStatus = "waiting_human"
		st.Status = StatusWaitingHuman
	} else if success {
		st.Checkpoint.EvalStatus = "pass"
	} else if humanGate {
		st.Checkpoint.EvalStatus = "human_gate"
	} else {
		st.Checkpoint.EvalStatus = "fail"
	}
	st.LastError = cr.Err
	st.LastEvalScore = evalScore
	idleProg := CycleProgress{
		Phase:     "idle",
		Iteration: iter,
		Note:      st.Checkpoint.EvalStatus,
		UpdatedAt: time.Now().UTC(),
	}
	if waitHuman {
		idleProg.Phase = "waiting_human"
		idleProg.StageKind = string(StageHumanGate)
		idleProg.StageID = st.Gate.StageID
	}
	if smRT != nil {
		idleProg = progressFromRoute(smRT, idleProg)
	}
	st.Progress = idleProg

	stopReason := m.evaluateStop(st, outcome, summary)
	if stopReason == "" && hadCritic && evalPass && st.Spec.Stop.OnSuccessN <= 0 && st.Spec.MaxIterations <= 0 {
		// Goal-style: critic pass ends the recursive goal when no schedule max set.
		if st.Spec.Interval == "" && st.Spec.Cron == "" {
			stopReason = "eval_pass"
		}
	}
	if stopReason == "" && waitHuman {
		stopReason = "waiting_human"
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

// completeParallel fans out N workers for one stage and critique-merges results.
func (m *Manager) completeParallel(ctx context.Context, st *LoopState, mod ModuleSpec, route RoutePref, reqID, turnID, prompt string) (string, int, RoutePref, error) {
	n := mod.Parallel
	if n <= 1 {
		return m.completeOnce(ctx, st, route, reqID, turnID, prompt, mod.Model)
	}
	if n > 4 {
		n = 4
	}
	roles := mod.Roles
	if len(roles) == 0 {
		roles = []string{string(swarm.RoleExec), string(swarm.RolePlan), string(swarm.RoleResearch), string(swarm.RoleWorker)}
	}
	workers := make([]swarm.Worker, n)
	for i := 0; i < n; i++ {
		i := i
		role := swarm.RoleWorker
		if i < len(roles) && roles[i] != "" {
			role = swarm.Role(roles[i])
		}
		rolePrompt := fmt.Sprintf("[%s worker %d/%d]\n%s", role, i+1, n, prompt)
		workers[i] = swarm.Worker{
			ID:    fmt.Sprintf("%s-%s-%d", mod.ID, role, i),
			Role:  role,
			Model: mod.Model,
			Run: func(wctx context.Context) (contextkit.Episode, error) {
				text, tokens, _, err := m.completeOnce(wctx, st, route, fmt.Sprintf("%s-w%d", reqID, i), turnID, rolePrompt, mod.Model)
				return contextkit.Episode{
					Summary: text,
					Tokens:  tokens,
					Model:   mod.Model,
					Reason:  "loop_parallel",
					Role:    string(role),
				}, err
			},
		}
	}
	m.setProgress(st, stagePhase(mod.Kind), mod, 0, st.Iteration, fmt.Sprintf("fan_out n=%d", n))
	results, err := swarm.FanOut(ctx, workers, swarm.Options{
		MaxWorkers: n,
		TurnID:     turnID,
	})
	merged := swarm.CritiqueMerge(results)
	tokens := merged.Tokens
	if tokens == 0 {
		tokens = len(merged.Summary) / 4
	}
	ok := 0
	for _, r := range results {
		if r.Err == nil && strings.TrimSpace(r.Episode.Summary) != "" {
			ok++
		}
	}
	if ok == 0 {
		if err != nil {
			return merged.Summary, tokens, route, err
		}
		return merged.Summary, tokens, route, fmt.Errorf("parallel stage %s: all workers failed", mod.ID)
	}
	return merged.Summary, tokens, route, nil
}

func (m *Manager) setProgress(st *LoopState, phase string, mod ModuleSpec, idx, iter int, note string) {
	if st == nil {
		return
	}
	st.Progress = CycleProgress{
		Phase:      phase,
		StageKind:  string(mod.Kind),
		StageID:    mod.ID,
		StageIndex: idx,
		Iteration:  iter,
		Note:       note,
		UpdatedAt:  time.Now().UTC(),
	}
	if m.Logs != nil {
		msg := phase + " " + string(mod.Kind)
		if mod.ID != "" {
			msg += " (" + mod.ID + ")"
		}
		if note != "" {
			msg += " -- " + note
		}
		m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "stage_start", msg, map[string]string{
			"phase":     phase,
			"stage":     string(mod.Kind),
			"stage_id":  mod.ID,
			"iteration": fmt.Sprintf("%d", iter),
		})
	}
	_ = m.Store.Save(st)
}

func stagePhase(k StageKind) string {
	switch k {
	case StageMemory:
		return "observe"
	case StageRouter:
		return "route"
	case StagePlanner:
		return "plan"
	case StageActor:
		return "act"
	case StageCritic:
		return "critique"
	case StageHumanGate:
		return "waiting_human"
	default:
		return string(k)
	}
}

func (m *Manager) emitRouteDecision(st *LoopState, rt *statemachine.Runtime, mod ModuleSpec) {
	if m.Logs == nil || st == nil || rt == nil {
		return
	}
	attrs := map[string]string{
		"stage_id":  mod.ID,
		"stage":     string(mod.Kind),
		"current":   string(rt.Current),
		"topology":  string(rt.Def.Topology),
		"branches":  summarizeBranch(rt),
	}
	if len(rt.Route.NextEdges) > 0 {
		attrs["next_edges"] = strings.Join(rt.Route.NextEdges, ",")
	}
	m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "route_decision",
		fmt.Sprintf("at %s next=%d", mod.ID, len(rt.Route.NextEdges)), attrs)
}

func (m *Manager) budgetOK(st *LoopState) bool {
	if m == nil || m.BudgetCheck == nil {
		return true
	}
	return m.BudgetCheck(st)
}

func (m *Manager) graphEventCount(turnID string) int {
	if m == nil || m.Graph == nil {
		return 0
	}
	if v, ok := m.Graph.Turn(turnID); ok && v.Stats != nil {
		return v.Stats.EventCount
	}
	// Fallback: blend store-wide relevancy into count proxy.
	r := m.Graph.RelevancyScore(turnID)
	return int(r * 20)
}

func humanToSM(st *LoopState) *statemachine.HumanDecision {
	if st == nil || st.Gate.Decision == "" {
		return nil
	}
	return &statemachine.HumanDecision{
		Approved: st.Gate.Decision == "approve",
		Comment:  st.Gate.Comment,
		At:       st.Gate.DecidedAt,
		Actor:    st.Gate.Actor,
		GateNode: statemachine.StateID(st.Gate.StageID),
	}
}
