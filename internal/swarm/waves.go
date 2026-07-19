package swarm

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/google/uuid"
)

// GraphContext is the dual-layer context surface used by multi-wave runs.
// Implemented by *contextgraph.Store via dashboard adapters.
type GraphContext interface {
	Query(turnID, q string, limit int) string
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
}

// WeaveWaves concatenates per-wave merged episodes then applies CritiqueMerge-style ranking.
// P0 weave: concatenate + critic (not full Slate episode programming).
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
		ep.Summary = "weave " + strings.Join(parts, " || ")
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

	base := req.RunRequest
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

	var allWaveResults [][]Result
	var waveMerges []contextkit.Episode
	var lastViews []ResultView
	var lastProgress DecisionRouteView
	spent := 0
	start := time.Now()
	goal := strings.TrimSpace(base.Prompt)

	for w := 0; w < nWaves; w++ {
		wavePrompt := goal
		if w > 0 {
			prior := seedFromGraph(r.GraphCtx, turnID, w-1)
			if prior == "" && len(waveMerges) > 0 {
				prior = waveMerges[len(waveMerges)-1].Summary
			}
			if prior != "" {
				wavePrompt = fmt.Sprintf("%s\n\n[prior_wave_%d]\n%s", goal, w-1, truncate(prior, 1600))
			}
		}
		waveReq := base
		waveReq.Prompt = wavePrompt
		waveReq.TurnID = turnID
		if r.Logs != nil {
			r.Logs.Info(agentlog.ScopeSwarm, turnID, "wave", fmt.Sprintf("wave %d/%d start", w+1, nWaves), map[string]string{
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

		waveRec := WaveRecord{
			Index:   w,
			Prompt:  wavePrompt,
			Results: resp.Results,
			Merged:  resp.Episode,
			At:      time.Now().UTC(),
		}
		if _, saveErr := r.Threads.AppendWave(threadID, turnID, goal, waveRec); saveErr != nil && r.Logs != nil {
			r.Logs.Error(agentlog.ScopeSwarm, turnID, "thread", "thread save: "+saveErr.Error(), nil)
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

	woven := WeaveWaves(waveMerges, allWaveResults)
	woven.ID = threadID + "-weave"
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
	}, nil
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
