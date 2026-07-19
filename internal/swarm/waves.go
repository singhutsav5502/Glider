package swarm

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/google/uuid"
)

// GraphContext is the dual-layer context surface used by multi-wave runs.
// Implemented by *contextgraph.Store via dashboard adapters.
type GraphContext interface {
	Query(turnID, q string, limit int) string
	PathSummary(turnID, from, to string) string
	WaveOutputs(turnID string, waveIndex int, limit int) []string
	RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []WaveWorkerOut)
	RecordEpisodeFact(turnID, episodeID, label, summary string)
}

// WaveWorkerOut is a graph-write DTO (avoids contextgraph importing swarm).
type WaveWorkerOut struct {
	WorkerID string
	Role     string
	Model    string
	Summary  string
	OK       bool
}

// RunWavesRequest runs N sequential FanOut waves on one durable thread.
// Wave N+1 seeds its prompt from prior wave outputs via the shared graph.
type RunWavesRequest struct {
	RunRequest
	// Waves is the number of sequential FanOut waves (default 2, max 4).
	Waves int `json:"waves,omitempty"`
	// ThreadID durable id (default = turn_id).
	ThreadID string `json:"thread_id,omitempty"`
	// WeavePolicy: concatenate | role_weighted | critic | conflict_callouts.
	WeavePolicy WeavePolicy `json:"weave_policy,omitempty"`
	// Decompose parses first plan-role / planner output into SubTasks for later waves.
	Decompose bool `json:"decompose,omitempty"`
	// SubTasks optional explicit prompts (overrides decompose when non-empty).
	SubTasks []backend.SubTask `json:"subtasks,omitempty"`
	// ResumeFrom skips wave indices already persisted (set by ResumeThread).
	ResumeFrom int `json:"resume_from,omitempty"`
}

// WeaveWaves concatenates per-wave merged episodes then applies CritiqueMerge-style ranking.
// Prefer ApplyWeavePolicy for P1 policies; this remains the critic default.
func WeaveWaves(waveMerges []contextkit.Episode, waveResults [][]Result) contextkit.Episode {
	var flat []Result
	for i, ep := range waveMerges {
		flat = append(flat, Result{
			WorkerID: fmt.Sprintf("wave-%d", i),
			Role:     Role(ep.Role),
			Model:    ep.Model,
			Episode:  ep,
		})
		if i < len(waveResults) {
			flat = append(flat, waveResults[i]...)
		}
	}
	if len(flat) == 0 {
		return contextkit.Episode{Summary: "weave: empty", Reason: "weave", Model: "glider-swarm"}
	}
	ep := CritiqueMerge(flat)
	ep.Reason = "weave"
	ep.ID = fmt.Sprintf("weave-%d", len(waveMerges))
	var parts []string
	for i, w := range waveMerges {
		sum := strings.TrimSpace(w.Summary)
		if sum == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[wave-%d] %s", i, truncate(sum, 200)))
	}
	if len(parts) > 0 {
		ep.Summary = "weave[critic] " + strings.Join(parts, " || ")
		if len(ep.Summary) > 1200 {
			ep.Summary = ep.Summary[:1200] + "…"
		}
	}
	return ep
}

// RunWaves executes multi-wave FanOut with durable thread persistence and graph seeding.
func (r *Runner) RunWaves(ctx context.Context, req RunWavesRequest) (*RunResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("swarm: nil runner")
	}
	if !r.Enabled.Load() {
		return nil, fmt.Errorf("swarm disabled (set orchestration.swarm.enabled)")
	}
	nWaves := req.Waves
	if nWaves <= 0 {
		nWaves = 2
	}
	if nWaves > 4 {
		nWaves = 4
	}
	policy := NormalizeWeavePolicy(req.WeavePolicy)

	base := req.RunRequest
	if base.TemplateID != "" && r.Templates != nil {
		if tpl, err := r.Templates.Get(base.TemplateID); err == nil && tpl != nil && tpl.Enabled {
			if nWaves <= 2 && tpl.Waves > 0 {
				nWaves = tpl.Waves
				if nWaves > 4 {
					nWaves = 4
				}
			}
			if req.WeavePolicy == "" && tpl.WeavePolicy != "" {
				policy = NormalizeWeavePolicy(tpl.WeavePolicy)
			}
			if !req.Decompose {
				req.Decompose = tpl.Decompose
			}
			if len(req.SubTasks) == 0 {
				for _, p := range tpl.SubTasks {
					req.SubTasks = append(req.SubTasks, backend.SubTask{Prompt: p})
				}
			}
		}
	}

	turnID := strings.TrimSpace(base.TurnID)
	if turnID == "" {
		turnID = "swarm-" + uuid.NewString()
		base.TurnID = turnID
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		threadID = turnID
	}
	if r.Threads == nil {
		r.Threads = NewThreadStore("")
	}

	var priorMerges []contextkit.Episode
	var priorResults [][]Result
	startWave := req.ResumeFrom
	if startWave < 0 {
		startWave = 0
	}
	if startWave > 0 {
		if st, err := r.Threads.Load(threadID); err == nil && st != nil {
			for _, w := range st.Waves {
				priorMerges = append(priorMerges, w.Merged)
				var wr []Result
				for _, v := range w.Results {
					res := Result{WorkerID: v.WorkerID, Role: Role(v.Role), Model: v.Model, Episode: v.Episode}
					if v.Err != "" {
						res.Err = fmt.Errorf("%s", v.Err)
					}
					wr = append(wr, res)
				}
				priorResults = append(priorResults, wr)
			}
			if st.Goal != "" && strings.TrimSpace(base.Prompt) == "" {
				base.Prompt = st.Goal
			}
			if st.TurnID != "" {
				turnID = st.TurnID
				base.TurnID = turnID
			}
			if policy == WeaveCritic && st.WeavePolicy != "" {
				policy = NormalizeWeavePolicy(st.WeavePolicy)
			}
			if len(req.SubTasks) == 0 && len(st.SubTasks) > 0 {
				for _, p := range st.SubTasks {
					req.SubTasks = append(req.SubTasks, backend.SubTask{Prompt: p})
				}
			}
		}
	}

	var allWaveResults [][]Result
	var waveMerges []contextkit.Episode
	allWaveResults = append(allWaveResults, priorResults...)
	waveMerges = append(waveMerges, priorMerges...)

	var lastViews []ResultView
	var lastProgress DecisionRouteView
	spent := 0
	start := time.Now()
	goal := strings.TrimSpace(base.Prompt)
	subtasks := append([]backend.SubTask{}, req.SubTasks...)
	endWave := startWave + nWaves
	if endWave > 4 {
		endWave = 4
	}

	for w := startWave; w < endWave; w++ {
		wavePrompt := goal
		if len(subtasks) > 0 && w < len(subtasks) {
			wavePrompt = FormatSubTaskPrompt(goal, w, subtasks[w])
		} else if w > 0 {
			prior := seedFromGraph(r.GraphCtx, turnID, w-1)
			if prior == "" && len(waveMerges) > 0 {
				prior = waveMerges[len(waveMerges)-1].Summary
			}
			pathHint := ""
			if r.GraphCtx != nil {
				pathHint = r.GraphCtx.PathSummary(turnID, fmt.Sprintf("wave-%d", w-1), fmt.Sprintf("wave-%d", w))
			}
			if prior != "" {
				wavePrompt = fmt.Sprintf("%s\n\n[prior_wave_%d]\n%s", goal, w-1, truncate(prior, 1600))
				if pathHint != "" && !strings.Contains(pathHint, "no link") && !strings.Contains(pathHint, "not found") {
					wavePrompt += "\n\n[path]\n" + truncate(pathHint, 400)
				}
			}
		}
		waveReq := base
		waveReq.Prompt = wavePrompt
		waveReq.TurnID = turnID
		// First wave with decompose: prefer plan role to produce SubTasks.
		if w == 0 && req.Decompose && len(subtasks) == 0 && len(waveReq.Roles) == 0 {
			waveReq.Roles = []string{string(RolePlan), string(RoleResearch)}
		}
		if r.Logs != nil {
			r.Logs.Info(agentlog.ScopeSwarm, threadID, "wave", fmt.Sprintf("wave %d start (thread)", w), map[string]string{
				"wave": strconv.Itoa(w), "thread": threadID, "turn": turnID,
				"policy": string(policy),
			})
			r.Logs.Info(agentlog.ScopeSwarm, turnID, "wave", fmt.Sprintf("wave %d/%d start", w+1, endWave), map[string]string{
				"wave": strconv.Itoa(w), "thread": threadID,
			})
		}

		resp, err := r.Run(ctx, waveReq)
		if resp == nil {
			return nil, err
		}
		results := make([]Result, len(resp.Results))
		for i, v := range resp.Results {
			results[i] = Result{
				WorkerID: v.WorkerID,
				Role:     Role(v.Role),
				Model:    v.Model,
				Episode:  v.Episode,
			}
			if v.Err != "" {
				results[i].Err = fmt.Errorf("%s", v.Err)
			}
		}
		allWaveResults = append(allWaveResults, results)
		waveMerges = append(waveMerges, resp.Episode)
		lastViews = resp.Results
		lastProgress = resp.Progress
		spent += resp.Tokens

		// Decompose after first wave when requested.
		if w == 0 && req.Decompose && len(subtasks) == 0 {
			planText := resp.Episode.Summary
			for _, res := range results {
				if res.Role == RolePlan || strings.EqualFold(string(res.Role), "planner") {
					if strings.TrimSpace(res.Episode.Summary) != "" {
						planText = res.Episode.Summary
						break
					}
				}
			}
			subtasks = DecomposeSubTasks(planText, endWave)
			if r.Logs != nil && len(subtasks) > 0 {
				r.Logs.Info(agentlog.ScopeSwarm, threadID, "decompose",
					fmt.Sprintf("planner decomposed %d subtasks", len(subtasks)),
					map[string]string{"thread": threadID, "wave": "0"})
			}
		}

		waveRec := WaveRecord{
			Index:   w,
			Prompt:  wavePrompt,
			Results: resp.Results,
			Merged:  resp.Episode,
			At:      time.Now().UTC(),
		}
		if st, saveErr := r.Threads.AppendWave(threadID, turnID, goal, waveRec); saveErr != nil && r.Logs != nil {
			r.Logs.Error(agentlog.ScopeSwarm, turnID, "thread", "thread save: "+saveErr.Error(), nil)
		} else if st != nil {
			st.WeavePolicy = policy
			st.Status = "running"
			if len(subtasks) > 0 {
				st.SubTasks = nil
				for _, s := range subtasks {
					st.SubTasks = append(st.SubTasks, s.Prompt)
				}
			}
			_ = r.Threads.Save(st)
		}

		if r.GraphCtx != nil {
			workers := make([]WaveWorkerOut, len(results))
			for i, res := range results {
				workers[i] = WaveWorkerOut{
					WorkerID: res.WorkerID,
					Role:     string(res.Role),
					Model:    res.Model,
					Summary:  res.Episode.Summary,
					OK:       res.Err == nil,
				}
			}
			r.GraphCtx.RecordThreadWave(turnID, threadID, w, resp.Episode.ID, resp.Episode.Summary, workers)
			r.GraphCtx.RecordEpisodeFact(turnID, resp.Episode.ID, fmt.Sprintf("wave-%d-merge", w), resp.Episode.Summary)
		}

		if err != nil && len(resp.Results) == 0 {
			return resp, err
		}
	}

	woven := ApplyWeavePolicy(policy, waveMerges, allWaveResults)
	woven.ID = threadID + "-weave"
	// Episode digest: tool/artifact hints from worker episodes.
	woven.Artifacts = episodeDigest(allWaveResults)
	summary := OrchestratorSummary(woven, flattenResults(allWaveResults))
	_ = r.Threads.SetMerged(threadID, woven, summary)

	if r.GraphCtx != nil {
		r.GraphCtx.RecordEpisodeFact(turnID, woven.ID, "weave", woven.Summary)
	}
	if r.Episodes != nil {
		sid := base.SessionID
		if sid == "" {
			sid = r.SessionID
		}
		if sid != "" {
			r.Episodes.RecordEpisode(sid, woven)
		}
	}
	if r.Logs != nil {
		r.Logs.Info(agentlog.ScopeSwarm, threadID, "weave", "weave done: "+truncate(summary, 120), map[string]string{
			"policy": string(policy), "waves": strconv.Itoa(len(waveMerges)),
		})
	}

	lastProgress.Current = "weave"
	lastProgress.PathTaken = append(append([]string{}, lastProgress.PathTaken...), "weave")
	return &RunResponse{
		TurnID:    turnID,
		Summary:   summary,
		Episode:   woven,
		Results:   lastViews,
		ElapsedMS: time.Since(start).Milliseconds(),
		Progress:  lastProgress,
		Tokens:    spent,
		ThreadID:  threadID,
		Waves:     len(waveMerges),
		Policy:    string(policy),
	}, nil
}

// ResumeThread continues a durable thread with additional FanOut waves, then re-weaves.
func (r *Runner) ResumeThread(ctx context.Context, threadID string, extraWaves int, policy WeavePolicy) (*RunResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("swarm: nil runner")
	}
	if r.Threads == nil {
		r.Threads = NewThreadStore("")
	}
	st, err := r.Threads.Load(threadID)
	if err != nil {
		return nil, err
	}
	if extraWaves <= 0 {
		extraWaves = 1
	}
	if policy == "" {
		policy = st.WeavePolicy
	}
	req := RunWavesRequest{
		RunRequest: RunRequest{
			Prompt: st.Goal,
			TurnID: st.TurnID,
		},
		Waves:      extraWaves,
		ThreadID:   threadID,
		WeavePolicy: policy,
		ResumeFrom: len(st.Waves),
		Decompose:  false,
	}
	for _, p := range st.SubTasks {
		req.SubTasks = append(req.SubTasks, backend.SubTask{Prompt: p})
	}
	return r.RunWaves(ctx, req)
}

func seedFromGraph(g GraphContext, turnID string, priorWave int) string {
	if g == nil {
		return ""
	}
	outs := g.WaveOutputs(turnID, priorWave, 12)
	if len(outs) > 0 {
		return strings.Join(outs, "\n")
	}
	q := g.Query(turnID, "wave", 12)
	if strings.Contains(q, "no hits") {
		return ""
	}
	return q
}

func flattenResults(waves [][]Result) []Result {
	var out []Result
	for _, w := range waves {
		out = append(out, w...)
	}
	return out
}

func episodeDigest(waves [][]Result) []string {
	var out []string
	seen := map[string]bool{}
	for _, results := range waves {
		for _, r := range results {
			for _, a := range r.Episode.Artifacts {
				a = strings.TrimSpace(a)
				if a == "" || seen[a] {
					continue
				}
				seen[a] = true
				out = append(out, a)
			}
			if len(out) >= 12 {
				return out
			}
		}
	}
	return out
}
