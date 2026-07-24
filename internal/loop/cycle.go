package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/plugin"
	"github.com/glider-ai/glider/internal/statemachine"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/google/uuid"
)

// Tool-loop step caps (model↔tool rounds inside RunAgentLoop).
// Audit-style stages need many list/grep/read rounds; 6 was too low and exhausted
// before critic could emit SCORE or workers finish under audit-target/.
const (
	toolLoopMaxStepsStage    = 20 // sequential planner/actor/critic stages (min floor)
	toolLoopMaxStepsParallel = 28 // parallel workers (grep/list/read heavy; 24–32 band)

	// Text retention for audit-sized actor/critic/merge output (hard caps, raised from 8–16k).
	stageTextCap    = 48000 // StageResult.Text + cycle summary
	stageOutcomeCap = 32000 // StageOutcome / LiveStages summaries (dashboard Copy logs)
	promptPlanCap   = 12000 // PLAN: block injected into later stages
	promptActorCap  = 24000 // ACTOR_OUTPUT: for critic
	hitlCriticCap   = 12000
	hitlActorCap    = 32000
	hitlPlanCap     = 8000

	// DefaultCompletionMaxTokens is max_tokens (Ollama num_predict) for hoop Completes
	// when RunnerConfig.DefaultMaxTokens is unset. Local models otherwise often stop
	// mid-sentence / mid-tool-JSON on a low server default.
	DefaultCompletionMaxTokens = 8192
)

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
	Iteration int           `json:"iteration"`
	Stages    []StageResult `json:"stages"`
	EvalScore float64       `json:"eval_score"`
	EvalPass  bool          `json:"eval_pass"`
	Success   bool          `json:"success"`
	Route     string        `json:"route"`
	LatencyMS int64         `json:"latency_ms"`
	Tokens    int           `json:"token_estimate"`
	EpisodeID string        `json:"episode_id,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	HumanGate bool          `json:"human_gate,omitempty"`
	Err       string        `json:"err,omitempty"`
	At        time.Time     `json:"at"`
}

// runCycle executes one Loop Engineering cycle for the hoop.
func (m *Manager) runCycle(ctx context.Context, st *LoopState) (CycleResult, string, error) {
	resuming := st.Cursor.Active && st.Checkpoint.WakeReason == "resume"
	if resuming {
		// Continue the same iteration after human_gate — do not bump Iteration.
		if st.Cursor.Iteration > 0 {
			st.Iteration = st.Cursor.Iteration
		}
	} else {
		st.Iteration++
	}
	iter := st.Iteration
	turnID := "loop:" + st.Spec.ID
	start := time.Now()
	goal := st.Spec.Goal
	if goal == "" {
		goal = st.Spec.Prompt
	}

	// Governance: hard budget already exceeded → stop immediately.
	if stop, reason := m.checkGovernance(st, 0, 0); stop {
		return CycleResult{Iteration: iter, Err: reason, At: time.Now().UTC()}, reason, fmt.Errorf("%s", reason)
	}
	m.recordCycleStart(st)
	if !resuming {
		st.LiveStages = nil
		_ = m.Store.Save(st)
	}
	if m.Tools != nil {
		if _, err := m.Tools.EnsureRunLayout(st.Spec.ID); err != nil && m.Logs != nil {
			m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, "artifacts", "ensure run layout: "+err.Error(), nil)
		}
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
		if resuming && len(st.Cursor.PathTaken) > 0 {
			smRT.Path = nil
			for _, id := range st.Cursor.PathTaken {
				smRT.Path = append(smRT.Path, statemachine.StateID(id))
			}
			if len(smRT.Path) > 0 {
				smRT.Current = smRT.Path[len(smRT.Path)-1]
			}
		}
		smRT.SetContext(statemachine.DecisionContext{
			BudgetOK:     m.budgetOK(st),
			RouterSignal: string(EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))),
			Relevancy:    relevancyHint(0, false, EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st)), 0),
			Human:        humanToSM(st),
		})
	}

	m.emit(st, contextgraph.EventLoopTick, map[string]string{
		"loop_id":   st.Spec.ID,
		"iteration": fmt.Sprintf("%d", iter),
		"kind":      "cycle",
		"resume":    fmt.Sprintf("%t", resuming),
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

	startIdx := 0
	if resuming {
		planText = st.Cursor.PlanText
		actorText = st.Cursor.ActorText
		criticText = st.Cursor.CriticText
		evalScore = st.Cursor.EvalScore
		evalPass = st.Cursor.EvalPass
		hadCritic = st.Cursor.HadCritic
		if st.Cursor.LastRoute != "" {
			lastRoute = RoutePref(st.Cursor.LastRoute)
		}
		startIdx = st.Cursor.ResumeIndex + 1
		if startIdx < 0 {
			startIdx = 0
		}
		// Clear cursor now that we've loaded mid-cycle state.
		st.Cursor = MachineCursor{}
		if m.Logs != nil {
			m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "hitl",
				fmt.Sprintf("mid-cycle resume from index %d", startIdx), map[string]string{
					"resume_index": fmt.Sprintf("%d", startIdx),
				})
		}
	}

	for stageIdx, mod := range modules {
		if stageIdx < startIdx {
			continue
		}
		if ctx.Err() != nil {
			cycleErr = ctx.Err()
			break
		}
		_ = mod.Normalize()
		if !mod.IsEnabled() {
			continue
		}
		// Soft budget: prefer local when configured.
		if st.Spend.SoftHit && st.Spec.Governance.PreferLocalOnSoft {
			lastRoute = RouteLocal
		}
		// Sync state machine current node + live DecisionRoute.
		if smRT != nil {
			smRT.Current = statemachine.StateID(mod.ID)
			if len(smRT.Path) == 0 || smRT.Path[len(smRT.Path)-1] != smRT.Current {
				smRT.Path = append(smRT.Path, smRT.Current)
			}
			smRT.Status = statemachine.StatusRunning
			smRT.SetContext(statemachine.DecisionContext{
				BudgetOK:     m.budgetOK(st) && !st.Spend.HardHit,
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

		hook := plugin.StageHook{
			HoopID:    st.Spec.ID,
			StageID:   mod.ID,
			StageKind: string(mod.Kind),
			Iteration: iter,
			Attrs:     map[string]string{"autonomy": string(stageAutonomy(st.Spec, mod))},
		}
		plugin.DispatchStageEnter(ctx, m.Plugins, hook)
		exitStageHooks := func() { plugin.DispatchStageExit(ctx, m.Plugins, hook) }

		// Per-stage human_gate / L3 hoop + stage autonomy L1 — pause without kind: human_gate.
		// Skip once on mid-cycle resume so approve re-enters the stage instead of re-pausing.
		skipStageGate := resuming && stageIdx == startIdx
		if !skipStageGate && mod.Kind != StageHumanGate && stageRequestsHumanGate(st.Spec, mod) {
			path := []string{}
			if smRT != nil {
				path = stateIDsToStrings(smRT.Path)
			}
			st.Cursor = MachineCursor{
				Active:        true,
				ResumeStageID: mod.ID,
				ResumeIndex:   stageIdx,
				Iteration:     iter,
				PlanText:      planText,
				ActorText:     actorText,
				CriticText:    criticText,
				PathTaken:     path,
				EvalScore:     evalScore,
				EvalPass:      evalPass,
				HadCritic:     hadCritic,
				LastRoute:     string(lastRoute),
			}
			reason := "stage human_gate — review prior output, then Approve or Reject"
			if mod.HumanGate {
				reason = "stage human_gate on " + mod.ID
			} else if st.Spec.Autonomy == AutonomyL3 && mod.Autonomy == AutonomyL1 {
				reason = "L3 hoop / L1 stage gate on " + mod.ID
			}
			openGate(st, mod, reason)
			st.Progress = progressFromRoute(smRT, st.Progress)
			st.Progress.Phase = "waiting_human"
			_ = m.Store.Save(st)
			if m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "hitl", "paused at stage gate "+mod.ID, map[string]string{
					"stage_id": mod.ID, "resume_index": fmt.Sprintf("%d", stageIdx),
					"autonomy": string(stageAutonomy(st.Spec, mod)),
				})
			}
			waitHuman = true
			gateText := st.Gate.Ask
			if gateText == "" {
				gateText = "waiting_human"
			}
			stages = append(stages, StageResult{Kind: StageHumanGate, ModuleID: mod.ID, Text: gateText})
			m.publishLiveStages(st, stages)
			exitStageHooks()
			break
		}

		// HITL first-class node: pause cycle until approve/resume.
		if mod.Kind == StageHumanGate {
			path := []string{}
			if smRT != nil {
				path = stateIDsToStrings(smRT.Path)
			}
			st.Cursor = MachineCursor{
				Active:        true,
				ResumeStageID: mod.ID,
				ResumeIndex:   stageIdx,
				Iteration:     iter,
				PlanText:      planText,
				ActorText:     actorText,
				CriticText:    criticText,
				PathTaken:     path,
				EvalScore:     evalScore,
				EvalPass:      evalPass,
				HadCritic:     hadCritic,
				LastRoute:     string(lastRoute),
			}
			openGate(st, mod, "human_gate stage — review prior stage output, then Approve or Reject")
			st.Progress = progressFromRoute(smRT, st.Progress)
			st.Progress.Phase = "waiting_human"
			_ = m.Store.Save(st)
			if m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "hitl", "paused at human_gate "+mod.ID, map[string]string{
					"stage_id": mod.ID, "resume_index": fmt.Sprintf("%d", stageIdx),
					"ask": truncate(st.Gate.Ask, 500),
				})
			}
			waitHuman = true
			gateText := st.Gate.Ask
			if gateText == "" {
				gateText = "waiting_human"
			}
			stages = append(stages, StageResult{Kind: StageHumanGate, ModuleID: mod.ID, Text: gateText})
			m.publishLiveStages(st, stages)
			exitStageHooks()
			break
		}

		// Node tools — parallel invoke + feed results into the model prompt / agent loop.
		// Critic defaults to no tools (completeOnce) so it scores ACTOR_OUTPUT instead of chatting.
		var toolBlock string
		var refs []tools.Ref
		wantTools := m.Tools != nil && (len(mod.Tools) > 0 ||
			mod.Kind == StageActor || mod.Kind == StagePlanner)
		if mod.Kind == StageCritic {
			wantTools = m.Tools != nil && len(mod.Tools) > 0
		}
		if wantTools {
			refs = m.filterToolRefs(st, mod.Kind, mod.Tools)
			if m.Tools != nil {
				refs = m.Tools.ExpandRefs(ctx, refs)
			}
			if len(refs) > 0 && !st.Spend.SoftHit {
				// Only blind-invoke read-only tools with workspace "." — never the goal.
				// Structured tools (git_clone, fs_write, MCP, grep) run via RunAgentLoop.
				blindRefs := tools.FilterBlindSafe(refs)
				var toolResults []tools.Result
				if len(blindRefs) > 0 {
					toolResults = m.Tools.InvokeAllParallel(ctx, blindRefs, tools.BlindPrepassInput())
					toolBlock = tools.FormatToolResults(toolResults)
					m.noteToolArtifacts(st, toolResults)
					m.recordClonePathsFromTools(st, turnID, toolResults)
				}
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
				m.indexToolGraphResults(turnID, toolResults)
			}
		}

		switch mod.Kind {
		case StageMemory, StageContext:
			digest := m.seedSharedContext(st, mod, turnID, goal, planText, actorText)
			text := "ok"
			if digest != "" {
				text = digest
			}
			stages = append(stages, StageResult{Kind: mod.Kind, ModuleID: mod.ID, Text: text})
			m.recordStageFeed(st, turnID, mod.ID, text)
			m.publishLiveStages(st, stages)
			exitStageHooks()
			continue
		case StageRouter:
			lastRoute = EffectiveRoute(st.Spec, st.Hoop, m.learningCfg(st))
			if mod.Route != "" {
				lastRoute = mod.Route
			}
			if st.Spend.SoftHit && st.Spec.Governance.PreferLocalOnSoft {
				lastRoute = RouteLocal
			}
			stages = append(stages, StageResult{
				Kind: StageRouter, ModuleID: mod.ID, Route: string(lastRoute),
				Text: "route=" + string(lastRoute),
			})
			m.recordStageFeed(st, turnID, mod.ID, "route="+string(lastRoute))
			m.publishLiveStages(st, stages)
			m.emit(st, contextgraph.EventRouteDecided, map[string]string{
				"loop_id": st.Spec.ID, "route": string(lastRoute), "stage": "router",
			})
			exitStageHooks()
			continue
		}

		route := lastRoute
		if mod.Route != "" {
			route = mod.Route
		}
		prompt := m.stagePrompt(st, mod, goal, planText, actorText)
		if toolBlock != "" {
			prompt = prompt + "\n\n" + toolBlock
		}
		reqID := fmt.Sprintf("loop-%s-%d-%s", st.Spec.ID, iter, mod.ID)
		t0 := time.Now()

		var text string
		var tokens int
		var used RoutePref
		var err error

		// Parallel actor (or any LLM stage with Parallel>1) → swarm fan-out + critique merge.
		// Workers get the same tool refs as sequential stages (agent loop), not blind writes.
		if mod.Parallel > 1 && (mod.Kind == StageActor || mod.Kind == StagePlanner) {
			text, tokens, used, err = m.completeParallel(ctx, st, mod, route, reqID, turnID, prompt, refs)
		} else if len(refs) > 0 && m.Tools != nil {
			text, tokens, used, err = m.completeWithTools(ctx, st, route, reqID, turnID, prompt, mod.Model, refs, toolLoopMaxStepsStage)
		} else {
			var format json.RawMessage
			if mod.Kind == StageCritic && route == RouteLocal {
				// Ollama format / JSON schema when local — see backend.CriticEvalFormat.
				format = backend.CriticEvalFormat()
			}
			text, tokens, used, err = m.completeOnce(ctx, st, route, reqID, turnID, prompt, mod.Model, format)
		}

		sr := StageResult{
			Kind:      mod.Kind,
			ModuleID:  mod.ID,
			Route:     string(used),
			Text:      truncate(text, stageTextCap),
			LatencyMS: time.Since(t0).Milliseconds(),
			Tokens:    tokens,
		}
		if err != nil {
			sr.Err = err.Error()
			stages = append(stages, sr)
			cycleErr = err
			if mod.Parallel <= 1 && st.Spec.FailPolicy == FailEscalate && route == RouteLocal {
				text2, tok2, used2, err2 := m.completeOnce(ctx, st, RouteCloud, reqID+"-esc", turnID, prompt, mod.Model, nil)
				if err2 == nil {
					sr.Text = truncate(text2, stageTextCap)
					sr.Tokens = tok2
					sr.Route = string(used2)
					sr.Err = ""
					sr.LatencyMS = time.Since(t0).Milliseconds()
					cycleErr = nil
					text, tokens, used = text2, tok2, used2
					stages[len(stages)-1] = sr
				} else {
					m.publishLiveStages(st, stages)
					exitStageHooks()
					break
				}
			} else {
				m.publishLiveStages(st, stages)
				exitStageHooks()
				break
			}
		} else {
			stages = append(stages, sr)
		}
		m.recordStageFeed(st, turnID, mod.ID, sr.Text)
		m.publishLiveStages(st, stages)
		totalTok += tokens
		m.addSpend(st, tokens)
		if stop, reason := m.checkGovernance(st, totalTok, time.Since(start).Milliseconds()); stop {
			cycleErr = fmt.Errorf("%s", reason)
			sr.Err = reason
			waitHuman = false
			exitStageHooks()
			break
		}
		lastRoute = used
		if m.Logs != nil {
			kind := "stage_end"
			msg := fmt.Sprintf("%s done route=%s %dms", mod.Kind, used, sr.LatencyMS)
			attrs := map[string]string{
				"stage": string(mod.Kind), "route": string(used), "module_id": mod.ID,
			}
			// Keep message short; put full body in attrs so dashboard "Full output" /
			// Copy logs can expand without the row embedding a mid-sentence clip.
			if sr.Err != "" {
				attrs["err"] = sr.Err
				m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, kind, msg, attrs)
			} else {
				if sr.Text != "" {
					attrs["text"] = sr.Text
					attrs["text_chars"] = fmt.Sprintf("%d", len(sr.Text))
				}
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, kind, msg, attrs)
			}
		}

		switch mod.Kind {
		case StagePlanner:
			// Drop rejection/budget noise so it cannot poison CONTEXT / later PLAN blocks.
			planText = sanitizePlanText(text)
		case StageActor:
			actorText = text
			// After clone/verify actors, persist shared clone_path for later CONTEXT / fan-out.
			if err == nil && (mod.ID == "verify_clone" || mod.ID == "clone_fetch" ||
				strings.Contains(text, "CLONE_OK") || strings.Contains(strings.ToLower(text), "cloned to")) {
				if p := extractClonedPath(text); p != "" {
					m.recordClonePath(st, turnID, p, "")
				} else if m.Tools != nil {
					m.recordClonePath(st, turnID, m.Tools.ScopeRel("audit-target"), "audit-target")
				}
			}
			// Prefer disk truth over model prose: if audit-target exists, record clone_path
			// even when the model invents CLONE_FAILED / "does not exist".
			if err == nil && m.Tools != nil && (mod.ID == "verify_clone" || mod.ID == "clone_fetch") {
				m.recordClonePathIfPresent(st, turnID, "audit-target")
				if clone := m.canonicalClonePath(st); clone != "" {
					note := "\n[Glider: Clone verified: YES at " + clone + " — report success/inventory; do not claim clone failed]"
					if !strings.Contains(text, "Clone verified: YES at") {
						text += note
						actorText = text
						sr.Text = truncate(text, stageTextCap)
						stages[len(stages)-1] = sr
					}
				}
			}
		case StageCritic:
			hadCritic = true
			// Map structured JSON {"score","reason"} onto SCORE:/REASON: for the eval path.
			text = normalizeCriticOutput(text)
			criticText = text
			var hasScore bool
			evalScore, hasScore = parseEvalScoreOK(text)
			if !hasScore {
				evalScore = 0
				sr.Err = "critic missing SCORE"
				criticText = strings.TrimSpace(text) + "\n[Glider: critic missing SCORE — treated as 0]"
			}
			sr.EvalScore = evalScore
			sr.Text = truncate(criticText, stageTextCap)
			stages[len(stages)-1] = sr
			min := mod.EvalMin
			if min <= 0 {
				min = 0.7
			}
			evalPass = hasScore && evalScore >= min
			if m.Logs != nil {
				attrs := map[string]string{
					"eval_score": fmt.Sprintf("%.3f", evalScore),
					"eval_pass":  fmt.Sprintf("%t", evalPass),
				}
				if !hasScore {
					attrs["err"] = "critic missing SCORE"
				}
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "eval", fmt.Sprintf("SCORE: %.3f pass=%t", evalScore, evalPass), attrs)
			}
		}
		exitStageHooks()
	}
	m.setProgress(st, "learn", ModuleSpec{Kind: StageMemory, ID: "learn"}, len(modules), iter, "persisting")

	epID := uuid.New().String()
	summary := truncate(firstNonEmpty(criticText, actorText, planText), stageTextCap)
	lat := time.Since(start).Milliseconds()
	success := cycleErr == nil && (!hadCritic || evalPass)
	// On failure, prefer a readable error summary over an empty/partial model dump.
	if cycleErr != nil {
		summary = truncate(cycleErr.Error(), stageOutcomeCap)
	}

	humanGate := false
	var criticMod ModuleSpec
	for _, s := range modules {
		if s.Kind == StageCritic {
			criticMod = s
			break
		}
	}
	if hadCritic && !evalPass && criticFailWantsGate(st.Spec, criticMod) {
		humanGate = true
	}
	// Critic-fail auto-gate: durable wait when configured (unless already paused mid-cycle).
	// Safety valve: do not re-open HITL forever when OnFailN consecutive failures already accrued
	// (waiting_human outcomes count as fails, so approve→same critic fail cannot soft-loop).
	if !waitHuman && humanGate {
		maxFail := st.Spec.Stop.OnFailN
		if maxFail <= 0 {
			maxFail = 3 // default HITL retry cap when on_fail_n unset
		}
		if st.ConsecutiveFail >= maxFail {
			humanGate = false
			msg := fmt.Sprintf("on_fail_n: critic score=%.3f after %d consecutive failures — stopping instead of re-opening HITL (fix tool/agent output or raise stop_conditions.on_fail_n)", evalScore, st.ConsecutiveFail)
			if cycleErr == nil {
				cycleErr = fmt.Errorf("%s", msg)
			}
			if m.Logs != nil {
				m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, "hitl", msg, map[string]string{
					"eval_score":       fmt.Sprintf("%.3f", evalScore),
					"consecutive_fail": fmt.Sprintf("%d", st.ConsecutiveFail),
					"on_fail_n":        fmt.Sprintf("%d", maxFail),
				})
			}
		} else {
			gateMod := ModuleSpec{Kind: StageHumanGate, ID: "human_gate", Name: "human_gate"}
			gateIdx := len(modules)
			for i, s := range modules {
				if s.Kind == StageHumanGate {
					gateMod = s
					gateIdx = i
					break
				}
			}
			path := []string{}
			if smRT != nil {
				path = stateIDsToStrings(smRT.Path)
			}
			st.Cursor = MachineCursor{
				Active:        true,
				ResumeStageID: gateMod.ID,
				ResumeIndex:   gateIdx,
				Iteration:     iter,
				PlanText:      planText,
				ActorText:     actorText,
				CriticText:    criticText,
				PathTaken:     path,
				EvalScore:     evalScore,
				EvalPass:      evalPass,
				HadCritic:     hadCritic,
				LastRoute:     string(lastRoute),
			}
			openGate(st, gateMod, fmt.Sprintf("critic fail score=%.3f — review actor output before continuing", evalScore))
			waitHuman = true
			if len(stages) > 0 {
				// Ensure live/outcome rail shows the ask, not just waiting_human.
				last := stages[len(stages)-1]
				if last.Kind != StageHumanGate {
					stages = append(stages, StageResult{Kind: StageHumanGate, ModuleID: gateMod.ID, Text: st.Gate.Ask})
				}
			} else if st.Gate.Ask != "" {
				stages = append(stages, StageResult{Kind: StageHumanGate, ModuleID: gateMod.ID, Text: st.Gate.Ask})
			}
			if fb := pickFeedbackTarget(st.Spec, evalScore, evalPass); fb != "" && m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "route_decision", "feedback candidate "+fb, map[string]string{
					"target": fb, "eval_score": fmt.Sprintf("%.3f", evalScore),
				})
			}
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
		g.RecordEpisodeFact(turnID, epID, "loop-cycle", summary)
		g.RecordFact(turnID, contextgraph.Fact{
			ID:         "thread-hoop-" + st.Spec.ID,
			Kind:       contextgraph.KindThread,
			Label:      "hoop " + st.Spec.ID,
			Provenance: contextgraph.ProvenanceRuntime,
			Attrs: map[string]string{
				"loop_id":    st.Spec.ID,
				"iteration":  fmt.Sprintf("%d", iter),
				"episode_id": epID,
			},
		})
		// Production PathSummary: planner → critic narrative for SM / tools.
		if path := g.PathSummary(turnID, "planner", "critic"); path != "" && !strings.Contains(path, "no link") {
			attrs["path_summary"] = truncate(path, 200)
			g.Append(contextgraph.Event{
				Kind:   contextgraph.EventKind("PathSummary"),
				TurnID: turnID,
				Actor:  "loop",
				Attrs:  map[string]string{"path": truncate(path, 240), "provenance": string(contextgraph.ProvenanceInferred)},
			})
		}
	}

	outcome := IterationOutcome{
		Iteration:     iter,
		Success:       success && !waitHuman, // HITL pause is not a "success" for stop / learning
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
			Kind:     string(sr.Kind),
			ModuleID: sr.ModuleID,
			Success:  sr.Err == "",
			Summary:  truncate(sr.Text, stageOutcomeCap),
			Err:      sr.Err,
		})
	}
	st.LiveStages = outcome.Stages
	m.syncRunArtifacts(st)
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

	stopReason := ""
	if waitHuman {
		// Mid-cycle HITL must not be overwritten by on_success_n / eval_pass.
		stopReason = "waiting_human"
	} else {
		stopReason = m.evaluateStop(st, outcome, summary)
		if stopReason == "" && strings.HasPrefix(cr.Err, "budget_exceeded") {
			stopReason = "budget_exceeded"
		}
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

// stageAutonomy returns the effective autonomy for a stage (stage override → hoop).
func stageAutonomy(spec LoopSpec, mod ModuleSpec) AutonomyLevel {
	if mod.Autonomy != "" {
		return mod.Autonomy
	}
	if spec.Autonomy != "" {
		return spec.Autonomy
	}
	return AutonomyL1
}

// stageRequestsHumanGate is true when the stage opts into HITL (field or L1 on an L3 hoop).
func stageRequestsHumanGate(spec LoopSpec, mod ModuleSpec) bool {
	if mod.Kind == StageHumanGate {
		return true
	}
	if mod.HumanGate {
		return true
	}
	// Per-stage L1 on an L3 hoop forces a gate before that stage (risky/ambiguous work).
	if spec.Autonomy == AutonomyL3 && mod.Autonomy == AutonomyL1 {
		return true
	}
	return false
}

// criticFailWantsGate decides whether a failed critic opens HITL.
// Hoop-level HumanGate / L1 still work; per-stage HumanGate or L1 autonomy also gates.
func criticFailWantsGate(spec LoopSpec, criticMod ModuleSpec) bool {
	if spec.HumanGate || spec.Autonomy == AutonomyL1 {
		return true
	}
	if criticMod.HumanGate || criticMod.Autonomy == AutonomyL1 {
		return true
	}
	// Scan actor stages for explicit human_gate / L1 when hoop is L2/L3.
	for _, s := range spec.Stages {
		if s.Kind == StageActor && (s.HumanGate || s.Autonomy == AutonomyL1) {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// applyDefaultMaxTokens sets CompletionRequest.MaxTokens for local/tool loops when unset.
func (m *Manager) applyDefaultMaxTokens(req *backend.CompletionRequest) {
	if req == nil || req.MaxTokens != nil {
		return
	}
	n := DefaultCompletionMaxTokens
	if m != nil && m.Cfg.DefaultMaxTokens > 0 {
		n = m.Cfg.DefaultMaxTokens
	}
	req.MaxTokens = &n
}

func (m *Manager) publishLiveStages(st *LoopState, stages []StageResult) {
	if st == nil {
		return
	}
	out := make([]StageOutcome, 0, len(stages))
	for _, sr := range stages {
		out = append(out, StageOutcome{
			Kind:     string(sr.Kind),
			ModuleID: sr.ModuleID,
			Success:  sr.Err == "",
			Summary:  truncate(sr.Text, stageOutcomeCap),
			Err:      sr.Err,
		})
	}
	st.LiveStages = out
	_ = m.Store.Save(st)
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
	case StageContext:
		return "context"
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
		"stage_id": mod.ID,
		"stage":    string(mod.Kind),
		"current":  string(rt.Current),
		"topology": string(rt.Def.Topology),
		"branches": summarizeBranch(rt),
	}
	if len(rt.Route.NextEdges) > 0 {
		attrs["next_edges"] = strings.Join(rt.Route.NextEdges, ",")
	}
	m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "route_decision",
		fmt.Sprintf("at %s next=%d", mod.ID, len(rt.Route.NextEdges)), attrs)
}

func (m *Manager) budgetOK(st *LoopState) bool {
	if st != nil && st.Spend.HardHit {
		return false
	}
	if m == nil || m.BudgetCheck == nil {
		return true
	}
	return m.BudgetCheck(st)
}

func (m *Manager) filterToolRefs(st *LoopState, kind StageKind, declared []ToolRef) []tools.Ref {
	deny := st.Spec.Governance.ToolDenylist
	seen := map[string]bool{}
	var refs []tools.Ref
	add := func(name, kindName, server, plugin string) {
		if name == "" || denylistHit(deny, name) {
			return
		}
		key := name + "|" + kindName + "|" + server
		if seen[key] || seen[name] {
			return
		}
		seen[key] = true
		seen[name] = true
		refs = append(refs, tools.Ref{Name: name, Kind: tools.Kind(kindName), Server: server, Plugin: plugin})
	}
	for _, t := range declared {
		add(t.Name, t.Kind, t.Server, t.Plugin)
	}
	if kind == StageCritic {
		// Default: no tools — critic scores ACTOR_OUTPUT via completeOnce (avoids chatty tool loops).
		// When YAML declares tools, keep them read-only (strip write/clone); do not auto-inject extras.
		if len(declared) == 0 {
			return nil
		}
		return stripWriteCloneToolRefs(refs)
	}
	// Always allow scratch + final artifact I/O + shared context query for processing stages.
	for _, name := range []string{"fs_read", "fs_write", "fs_list", "artifact_write", "context_query"} {
		add(name, string(tools.KindBuiltin), "", "")
	}
	return refs
}

// stripWriteCloneToolRefs removes mutating builtins from a ref list (used for critics).
func stripWriteCloneToolRefs(refs []tools.Ref) []tools.Ref {
	var out []tools.Ref
	for _, r := range refs {
		switch r.Name {
		case "fs_write", "artifact_write", "git_clone":
			continue
		default:
			out = append(out, r)
		}
	}
	return out
}

// sanitizePlanText drops planner output that is mostly tool-rejection / budget noise
// so it cannot seed CONTEXT or later PLAN prompts.
func sanitizePlanText(plan string) string {
	plan = strings.TrimSpace(plan)
	if plan == "" || planLooksPoisoned(plan) {
		return ""
	}
	return plan
}

func planLooksPoisoned(plan string) bool {
	lower := strings.ToLower(plan)
	if strings.Contains(lower, "not allowed in this stage") ||
		strings.Contains(lower, "tool loop budget exhausted") {
		return true
	}
	// Error narratives that copied undeclared-tool rejections into "Clone result" / findings JSON.
	if strings.Contains(lower, "git_clone") &&
		(strings.Contains(lower, "error:") || strings.Contains(lower, "not allowed") ||
			strings.Contains(lower, `"clone result"`) || strings.Contains(lower, "clone result")) {
		return true
	}
	// Empty findings-only error blobs (no real plan steps).
	trimmed := strings.TrimSpace(plan)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		if strings.Contains(lower, `"error"`) &&
			(strings.Contains(lower, "findings") || strings.Contains(lower, "git_clone") ||
				strings.Contains(lower, "not allowed")) {
			return true
		}
		// Pure error JSON with no numbered plan steps.
		if strings.Contains(lower, `"error"`) && !containsPlanSteps(plan) {
			return true
		}
		// Planner/actor dumped a tool-call payload (esp. artifact_write) instead of a plan.
		if planLooksLikeToolCallJSON(trimmed) {
			return true
		}
	}
	return false
}

// planLooksLikeToolCallJSON detects plans that are primarily a tool invocation dump
// (OpenAI tool_calls, or artifact_write/fs_write argument objects) rather than steps.
func planLooksLikeToolCallJSON(plan string) bool {
	lower := strings.ToLower(strings.TrimSpace(plan))
	if lower == "" || !strings.HasPrefix(lower, "{") {
		return false
	}
	if containsPlanSteps(plan) {
		return false
	}
	// Explicit tool name in the blob.
	if strings.Contains(lower, "artifact_write") ||
		(strings.Contains(lower, `"name"`) && (strings.Contains(lower, `"fs_write"`) || strings.Contains(lower, `"git_clone"`))) {
		return true
	}
	if strings.Contains(lower, "tool_calls") || strings.Contains(lower, `"function"`) {
		if strings.Contains(lower, "artifact_write") || strings.Contains(lower, "fs_write") ||
			strings.Contains(lower, `"arguments"`) {
			return true
		}
	}
	// Bare artifact_write args: {"kind":"out|work","path":"...","content":"..."}
	hasKind := strings.Contains(lower, `"kind"`) && (strings.Contains(lower, `"out"`) || strings.Contains(lower, `"work"`))
	hasPath := strings.Contains(lower, `"path"`)
	hasContent := strings.Contains(lower, `"content"`)
	if hasKind && hasPath && hasContent {
		return true
	}
	return false
}

func containsPlanSteps(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "1)") || strings.Contains(lower, "1.") ||
		strings.Contains(lower, "step 1") || strings.Contains(lower, "- clone")
}

// noteToolArtifacts records workspace-relative paths from successful write/clone tools.
func (m *Manager) noteToolArtifacts(st *LoopState, results []tools.Result) {
	if st == nil {
		return
	}
	for _, tr := range results {
		if !tr.OK {
			continue
		}
		switch tr.Name {
		case "fs_write", "artifact_write", "git_clone":
			if p := parseArtifactPath(tr.Output); p != "" {
				st.Artifacts = appendUniquePath(st.Artifacts, p)
			}
		}
	}
}

func parseArtifactPath(out string) string {
	out = strings.TrimSpace(out)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "wrote artifact ") {
			rest := strings.TrimPrefix(line, "wrote artifact ")
			p, _, _ := strings.Cut(rest, " ")
			return filepath.ToSlash(strings.TrimSpace(p))
		}
		if strings.HasPrefix(line, "wrote ") {
			return filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(line, "wrote ")))
		}
		if strings.HasPrefix(line, "cloned to ") {
			return filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(line, "cloned to ")))
		}
	}
	return ""
}

func appendUniquePath(list []string, p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return list
	}
	for _, x := range list {
		if x == p {
			return list
		}
	}
	return append(list, p)
}

// syncRunArtifacts merges files under the run's work/ and out/ into state.
func (m *Manager) syncRunArtifacts(st *LoopState) {
	if m == nil || m.Tools == nil || st == nil {
		return
	}
	lay := tools.LayoutForRun(m.Tools.Workspace(), st.Spec.ID)
	for _, rel := range []string{lay.RelOut, lay.RelWork} {
		files, err := tools.WalkFiles(m.Tools.Workspace(), rel, 100)
		if err != nil {
			continue
		}
		for _, f := range files {
			st.Artifacts = appendUniquePath(st.Artifacts, f)
		}
	}
}

func (m *Manager) addSpend(st *LoopState, tokens int) {
	if st == nil || tokens <= 0 {
		return
	}
	st.Spend.Tokens += tokens
	st.Spend.CostUSD += st.Spec.Governance.estimateCost(tokens)
	st.Spend.LastCheck = time.Now().UTC()
	g := st.Spec.Governance
	if g.SoftTokens > 0 && st.Spend.Tokens >= g.SoftTokens {
		st.Spend.SoftHit = true
	}
	if g.SoftCostUSD > 0 && st.Spend.CostUSD >= g.SoftCostUSD {
		st.Spend.SoftHit = true
	}
	if g.HardTokens > 0 && st.Spend.Tokens >= g.HardTokens {
		st.Spend.HardHit = true
	}
	if g.HardCostUSD > 0 && st.Spend.CostUSD >= g.HardCostUSD {
		st.Spend.HardHit = true
	}
}

func (m *Manager) recordCycleStart(st *LoopState) {
	if st == nil {
		return
	}
	now := time.Now().UnixMilli()
	st.Spend.CycleStarts = append(st.Spend.CycleStarts, now)
	// Keep 2 minutes of window.
	cut := now - 120_000
	var kept []int64
	for _, t := range st.Spend.CycleStarts {
		if t >= cut {
			kept = append(kept, t)
		}
	}
	st.Spend.CycleStarts = kept
}

func parseToolCallsFromText(text string) []tools.ToolCallDelta {
	const mark = "__TOOL_CALLS__:"
	if i := strings.Index(text, mark); i >= 0 {
		raw := strings.TrimSpace(text[i+len(mark):])
		var deltas []backend.ToolCallDelta
		if json.Unmarshal([]byte(raw), &deltas) == nil {
			var out []tools.ToolCallDelta
			for _, d := range deltas {
				name, args := "", ""
				if d.Function != nil {
					name = d.Function.Name
					args = d.Function.Arguments
				}
				if name == "" {
					continue
				}
				out = append(out, tools.ToolCallDelta{ID: d.ID, Name: name, Arguments: args})
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// Local models often emit tool JSON as assistant text — parse & execute.
	return tools.ParseTextToolCalls(text)
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

// Thin Manager wrappers — bodies live on CycleExecutor / prompt.go / CheckGovernance.

func (m *Manager) completeOnce(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, format json.RawMessage) (string, int, RoutePref, error) {
	if m == nil {
		return "", 0, route, errCycleExecutorNil
	}
	return m.Exec().CompleteOnce(ctx, st, route, reqID, turnID, prompt, model, format)
}

func (m *Manager) completeWithTools(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, refs []tools.Ref, maxSteps int) (string, int, RoutePref, error) {
	if m == nil {
		return "", 0, route, errCycleExecutorNil
	}
	return m.Exec().CompleteWithTools(ctx, st, route, reqID, turnID, prompt, model, refs, maxSteps)
}

func (m *Manager) completeOnceWithTools(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, toolsJSON json.RawMessage) (string, int, RoutePref, error) {
	if m == nil {
		return "", 0, route, errCycleExecutorNil
	}
	return m.Exec().completeOnceWithTools(ctx, st, route, reqID, turnID, prompt, model, toolsJSON)
}

func (m *Manager) completeParallel(ctx context.Context, st *LoopState, mod ModuleSpec, route RoutePref, reqID, turnID, prompt string, refs []tools.Ref) (string, int, RoutePref, error) {
	if m == nil {
		return "", 0, route, errCycleExecutorNil
	}
	return m.Exec().CompleteParallel(ctx, st, mod, route, reqID, turnID, prompt, refs)
}

func (m *Manager) completeParallelSwarm(ctx context.Context, st *LoopState, mod ModuleSpec, route RoutePref, turnID, prompt string, refs []tools.Ref) (string, int, RoutePref, error) {
	if m == nil {
		return "", 0, route, errCycleExecutorNil
	}
	return m.Exec().CompleteParallelSwarm(ctx, st, mod, route, turnID, prompt, refs)
}
