package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/swarm"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/google/uuid"
)

// CycleExecutor owns stage LLM + tool-loop + parallel completion (SRP extract from Manager).
// runCycle orchestration stays on Manager; call sites use Exec() or Manager thin wrappers.
type CycleExecutor struct {
	mgr *Manager
}

// Exec returns the cycle executor bound to this manager (nil-safe).
func (m *Manager) Exec() *CycleExecutor {
	if m == nil {
		return nil
	}
	return &CycleExecutor{mgr: m}
}

// Manager returns the owning Manager (for gradual migration of call sites).
func (e *CycleExecutor) Manager() *Manager {
	if e == nil {
		return nil
	}
	return e.mgr
}

var errCycleExecutorNil = errString("loop: CycleExecutor is nil")

type errString string

func (e errString) Error() string { return string(e) }

// completeOnce forces route via /local|/cloud prefix and drains the harness stream.
// format is optional Ollama-native structured output (json.RawMessage of "json" or a JSON schema);
// only the Ollama backend wires it onto the request body's format field.
func (e *CycleExecutor) CompleteOnce(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, format json.RawMessage) (text string, tokens int, used RoutePref, err error) {
	if e == nil || e.mgr == nil {
		return "", 0, route, errCycleExecutorNil
	}
	m := e.mgr

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
		Format:   format,
		Metadata: backend.RequestMetadata{RequestID: reqID},
	}
	m.applyDefaultMaxTokens(req)
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

// completeWithTools runs an OpenAI-tools agentic loop via the harness Completer.
// maxSteps caps model↔tool rounds (see toolLoopMaxStepsStage / toolLoopMaxStepsParallel).
func (e *CycleExecutor) CompleteWithTools(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, refs []tools.Ref, maxSteps int) (string, int, RoutePref, error) {
	if e == nil || e.mgr == nil {
		return "", 0, route, errCycleExecutorNil
	}
	m := e.mgr

	if m.Tools == nil {
		return m.completeOnce(ctx, st, route, reqID, turnID, prompt, model, nil)
	}
	if maxSteps <= 0 {
		maxSteps = toolLoopMaxStepsStage
	}
	used := route
	sys := "You are a Glider hoop stage. Use tools when helpful, then answer."
	loopRes, err := m.Tools.RunAgentLoop(ctx, sys, prompt, tools.AgentLoopOpts{
		Refs:     refs,
		MaxSteps: maxSteps,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []tools.ToolCallDelta, error) {
			// Flatten messages into a single user prompt for backends that strip multi-turn,
			// while still attaching tools[] for Path A compatible backends.
			var b strings.Builder
			for _, msg := range messages {
				role, _ := msg["role"].(string)
				content, _ := msg["content"].(string)
				b.WriteString(strings.ToUpper(role))
				b.WriteString(":\n")
				b.WriteString(content)
				b.WriteString("\n\n")
				if tcs, ok := msg["tool_calls"]; ok {
					raw, _ := json.Marshal(tcs)
					b.WriteString("TOOL_CALLS:\n")
					b.Write(raw)
					b.WriteString("\n\n")
				}
			}
			text, tokens, _, cerr := m.completeOnceWithTools(ctx, st, route, reqID, turnID, b.String(), model, toolsJSON)
			_ = tokens
			if cerr != nil {
				return "", nil, cerr
			}
			calls := parseToolCallsFromText(text)
			if len(calls) > 0 {
				return text, calls, nil
			}
			return text, nil, nil
		},
	})
	if err != nil {
		return "", 0, used, err
	}
	if strings.Contains(loopRes.Text, "tool loop budget exhausted") && m.Logs != nil {
		m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "tool_loop", loopRes.Text, map[string]string{
			"req_id": reqID, "steps": fmt.Sprintf("%d", loopRes.Steps), "max_steps": fmt.Sprintf("%d", maxSteps),
		})
	}
	m.noteToolArtifacts(st, loopRes.Results)
	m.recordClonePathsFromTools(st, turnID, loopRes.Results)
	m.indexToolGraphResults(turnID, loopRes.Results)
	tok := len(loopRes.Text) / 4
	for _, r := range loopRes.Results {
		tok += len(r.Output) / 8
	}
	return loopRes.Text, tok, used, nil
}

func (e *CycleExecutor) completeOnceWithTools(ctx context.Context, st *LoopState, route RoutePref, reqID, turnID, prompt, model string, toolsJSON json.RawMessage) (string, int, RoutePref, error) {
	if e == nil || e.mgr == nil {
		return "", 0, route, errCycleExecutorNil
	}
	m := e.mgr

	used := route
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
		Tools:    toolsJSON,
		Metadata: backend.RequestMetadata{RequestID: reqID},
	}
	m.applyDefaultMaxTokens(req)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://glider.loop/v1/chat/completions", nil)
	httpReq.Header.Set("X-Glider-Loop-ID", st.Spec.ID)
	httpReq.Header.Set("X-Glider-Turn-ID", turnID)
	httpReq.Header.Set("X-Glider-Loop-Kind", "engineering_cycle")

	ch, err := m.Complete.Complete(httpReq, req)
	if err != nil {
		// Fallback without tools if backend rejects tools[].
		if backend.IsToolsUnsupported(err) {
			return m.completeOnce(ctx, st, route, reqID, turnID, prompt, model, nil)
		}
		return "", req.Metadata.EstimatedTokens, used, err
	}
	var b strings.Builder
	var toolBuf []backend.ToolCallDelta
	for chunk := range ch {
		b.WriteString(chunk.Content)
		if len(chunk.ToolCalls) > 0 {
			toolBuf = append(toolBuf, chunk.ToolCalls...)
		}
		if ctx.Err() != nil {
			return b.String(), req.Metadata.EstimatedTokens, used, ctx.Err()
		}
	}
	text := b.String()
	if len(toolBuf) > 0 {
		// Serialize tool calls for parseToolCallsFromText / agent loop.
		raw, _ := json.Marshal(toolBuf)
		text = text + "\n__TOOL_CALLS__:" + string(raw)
	}
	tokens := req.Metadata.EstimatedTokens
	if tokens == 0 {
		tokens = len(text) / 4
	}
	return text, tokens, used, nil
}

// completeParallel fans out N workers for one stage and critique-merges results.
// Default parallel_mode (fanout) uses in-process FanOut + CritiqueMerge.
// parallel_mode: swarm nests swarm.Runner.Run / RunWaves (explicit error if unavailable).
// When refs are non-empty, each fanout worker runs the agentic tool loop (same as sequential stages).
func (e *CycleExecutor) CompleteParallel(ctx context.Context, st *LoopState, mod ModuleSpec, route RoutePref, reqID, turnID, prompt string, refs []tools.Ref) (string, int, RoutePref, error) {
	if e == nil || e.mgr == nil {
		return "", 0, route, errCycleExecutorNil
	}
	m := e.mgr

	n := mod.Parallel
	if n <= 1 {
		if len(refs) > 0 && m.Tools != nil {
			return m.completeWithTools(ctx, st, route, reqID, turnID, prompt, mod.Model, refs, toolLoopMaxStepsStage)
		}
		return m.completeOnce(ctx, st, route, reqID, turnID, prompt, mod.Model, nil)
	}
	mode := strings.ToLower(strings.TrimSpace(mod.ParallelMode))
	if mode == "" {
		mode = ParallelModeFanout
	}
	if mode == ParallelModeSwarm {
		return m.completeParallelSwarm(ctx, st, mod, route, turnID, prompt, refs)
	}
	if n > 4 {
		n = 4
	}
	roles := mod.Roles
	if len(roles) == 0 {
		roles = []string{string(swarm.RoleExec), string(swarm.RolePlan), string(swarm.RoleResearch), string(swarm.RoleWorker)}
	}
	workspace := ""
	if m.Tools != nil {
		workspace = m.Tools.Workspace()
	}
	gitRoot := workspace
	if gitRoot == "" {
		gitRoot = "."
	}
	isolations, isoErr := isolateParallelWorkers(workspace, st.Spec.ID, n, m.Cfg.Worktrees, gitRoot)
	if isoErr != nil && m.Logs != nil {
		m.Logs.Error(agentlog.ScopeHoop, st.Spec.ID, "worktree", "isolate workers: "+isoErr.Error(), nil)
	}
	workers := make([]swarm.Worker, n)
	for i := 0; i < n; i++ {
		i := i
		role := swarm.RoleWorker
		if i < len(roles) && roles[i] != "" {
			role = swarm.Role(roles[i])
		}
		rolePrompt := fmt.Sprintf("[%s worker %d/%d]\n%s", role, i+1, n, prompt)
		if i < len(isolations) {
			if hint := isolationPromptHint(isolations[i]); hint != "" {
				rolePrompt = rolePrompt + "\n\n" + hint
			}
			if isolations[i].Note != "" && m.Logs != nil {
				m.Logs.Info(agentlog.ScopeHoop, st.Spec.ID, "worktree",
					fmt.Sprintf("worker %d: %s → %s", i, isolations[i].Note, isolations[i].RelWork), nil)
			}
		}
		workers[i] = swarm.Worker{
			ID:    fmt.Sprintf("%s-%s-%d", mod.ID, role, i),
			Role:  role,
			Model: mod.Model,
			Run: func(wctx context.Context) (contextkit.Episode, error) {
				wid := fmt.Sprintf("%s-w%d", reqID, i)
				var text string
				var tokens int
				var err error
				if len(refs) > 0 && m.Tools != nil {
					// Parallel auditors need more tool rounds (grep/list/read) than a single actor.
					text, tokens, _, err = m.completeWithTools(wctx, st, route, wid, turnID, rolePrompt, mod.Model, refs, toolLoopMaxStepsParallel)
				} else {
					text, tokens, _, err = m.completeOnce(wctx, st, route, wid, turnID, rolePrompt, mod.Model, nil)
				}
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
	if g := m.Graph; g != nil {
		ws := make([]contextgraph.WaveWorker, len(results))
		for i, r := range results {
			ws[i] = contextgraph.WaveWorker{
				WorkerID: r.WorkerID,
				Role:     string(r.Role),
				Model:    r.Model,
				Summary:  r.Episode.Summary,
				OK:       r.Err == nil,
			}
		}
		threadID := "hoop-" + st.Spec.ID
		g.RecordThreadWave(turnID, threadID, st.Iteration, merged.ID, merged.Summary, ws)
		g.RecordEpisodeFact(turnID, merged.ID, "hoop-parallel-merge", merged.Summary)
	}
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

// completeParallelSwarm nests swarm.Runner for a hoop stage (parallel_mode: swarm).
// Soft-fails with a clear error when Swarm is nil or disabled — does not silently fall back to fanout.
func (e *CycleExecutor) CompleteParallelSwarm(ctx context.Context, st *LoopState, mod ModuleSpec, route RoutePref, turnID, prompt string, refs []tools.Ref) (string, int, RoutePref, error) {
	if e == nil || e.mgr == nil {
		return "", 0, route, errCycleExecutorNil
	}
	m := e.mgr

	if m.Swarm == nil || !m.Swarm.IsEnabled() {
		return "", 0, route, fmt.Errorf("parallel_mode swarm: swarm runner unavailable or disabled (set orchestration.swarm.enabled and wire Manager.Swarm)")
	}
	n := mod.Parallel
	if n > 4 {
		n = 4
	}
	roles := append([]string(nil), mod.Roles...)
	nestedTurn := fmt.Sprintf("loop:%s:stage:%s:%s", st.Spec.ID, mod.ID, uuid.NewString()[:8])
	preferLocal := route == RouteLocal
	req := swarm.RunRequest{
		Prompt:      prompt,
		Roles:       roles,
		MaxWorkers:  n,
		TurnID:      nestedTurn,
		PreferLocal: preferLocal,
		Route:       string(route),
		SessionID:   st.Spec.ID,
		Tools:       append([]tools.Ref(nil), refs...),
	}
	if mod.Model != "" {
		req.Models = []string{mod.Model}
	}
	waves := 0
	weavePolicy := ""
	if mod.Swarm != nil {
		req.TemplateID = mod.Swarm.TemplateID
		waves = mod.Swarm.Waves
		weavePolicy = mod.Swarm.WeavePolicy
		if mod.Swarm.PreferLocal {
			req.PreferLocal = true
		}
		if len(mod.Swarm.Models) > 0 {
			req.Models = append([]string(nil), mod.Swarm.Models...)
		}
	}

	prevRunID := ""
	if m.Tools != nil {
		prevRunID = m.Tools.RunID()
		m.Tools.SetRunID(nestedTurn)
		defer m.Tools.SetRunID(prevRunID)
	}

	m.setProgress(st, stagePhase(mod.Kind), mod, 0, st.Iteration, fmt.Sprintf("swarm nested turn=%s waves=%d", nestedTurn, waves))

	var (
		resp *swarm.RunResponse
		err  error
	)
	if waves > 1 {
		resp, err = m.Swarm.RunWaves(ctx, swarm.RunWavesRequest{
			RunRequest:  req,
			Waves:       waves,
			ThreadID:    "hoop-" + st.Spec.ID,
			WeavePolicy: swarm.WeavePolicy(weavePolicy),
		})
	} else {
		resp, err = m.Swarm.Run(ctx, req)
	}
	if resp == nil {
		if err != nil {
			return "", 0, route, err
		}
		return "", 0, route, fmt.Errorf("parallel_mode swarm: empty response")
	}
	summary := strings.TrimSpace(resp.Summary)
	if summary == "" {
		summary = strings.TrimSpace(resp.Episode.Summary)
	}
	tokens := resp.Tokens
	if tokens == 0 {
		tokens = resp.Episode.Tokens
	}
	if tokens == 0 {
		tokens = len(summary) / 4
	}
	if g := m.Graph; g != nil {
		ws := make([]contextgraph.WaveWorker, len(resp.Results))
		for i, r := range resp.Results {
			ws[i] = contextgraph.WaveWorker{
				WorkerID: r.WorkerID,
				Role:     r.Role,
				Model:    r.Model,
				Summary:  r.Summary,
				OK:       r.Err == "",
			}
		}
		threadID := "hoop-" + st.Spec.ID
		g.RecordThreadWave(turnID, threadID, st.Iteration, nestedTurn+"-merge", summary, ws)
		g.RecordEpisodeFact(turnID, nestedTurn+"-merge", "hoop-swarm-merge", summary)
	}
	ok := 0
	for _, r := range resp.Results {
		if r.Err == "" && strings.TrimSpace(r.Summary) != "" {
			ok++
		}
	}
	if ok == 0 {
		if err != nil {
			return summary, tokens, route, err
		}
		return summary, tokens, route, fmt.Errorf("parallel_mode swarm stage %s: all workers failed", mod.ID)
	}
	return summary, tokens, route, err
}
