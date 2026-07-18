package swarm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/google/uuid"
)

// WorkerFn executes one role/model prompt and returns an Episode.
type WorkerFn func(ctx context.Context, role Role, model, prompt string) (contextkit.Episode, error)

// GraphSink records fan-out events (implemented by *contextgraph.Store).
type GraphSink interface {
	AppendTurn(turnID, workerID, role, model string, ok bool, summary string)
}

// RunRequest is the POST /api/swarm/run body.
type RunRequest struct {
	Prompt      string   `json:"prompt"`
	Roles       []string `json:"roles,omitempty"`
	Models      []string `json:"models,omitempty"`
	MaxWorkers  int      `json:"max_workers,omitempty"`
	TurnID      string   `json:"turn_id,omitempty"`
	TemplateID  string   `json:"template_id,omitempty"`
	PreferLocal bool     `json:"prefer_local,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
}

// RunResponse is the orchestrator-facing merge result.
type RunResponse struct {
	TurnID    string              `json:"turn_id"`
	Summary   string              `json:"summary"`
	Episode   contextkit.Episode  `json:"episode"`
	Results   []ResultView        `json:"results"`
	ElapsedMS int64               `json:"elapsed_ms"`
}

// ResultView is a JSON-safe worker result.
type ResultView struct {
	WorkerID string             `json:"worker_id"`
	Role     string             `json:"role,omitempty"`
	Model    string             `json:"model,omitempty"`
	Summary  string             `json:"summary,omitempty"`
	Tokens   int                `json:"tokens,omitempty"`
	Err      string             `json:"err,omitempty"`
	Episode  contextkit.Episode `json:"episode,omitempty"`
}

// Runner fans out workers via FanOut + MergeResults. Requires Enabled.
type Runner struct {
	Enabled      atomic.Bool
	Opts         Options
	WorkerFn     WorkerFn
	Graph        GraphSink
	Episodes     *contextkit.Store
	Templates    *TemplateStore
	SessionID    string
	DefaultModel string
}

// ApplyOpts hot-swaps concurrency bounds.
func (r *Runner) ApplyOpts(opts Options) {
	if r == nil {
		return
	}
	r.Opts = opts
}

// SetEnabled toggles the swarm API / runner gate.
func (r *Runner) SetEnabled(v bool) {
	if r != nil {
		r.Enabled.Store(v)
	}
}

// IsEnabled reports whether swarm run is allowed.
func (r *Runner) IsEnabled() bool {
	return r != nil && r.Enabled.Load()
}

// Run executes a fan-out wave and returns a merged orchestrator-facing summary.
func (r *Runner) Run(ctx context.Context, req RunRequest) (*RunResponse, error) {
	if r == nil {
		return nil, fmt.Errorf("swarm: nil runner")
	}
	if !r.Enabled.Load() {
		return nil, fmt.Errorf("swarm disabled (set orchestration.swarm.enabled)")
	}
	if r.WorkerFn == nil {
		return nil, fmt.Errorf("swarm: no worker function")
	}
	prompt := strings.TrimSpace(req.Prompt)
	roles := req.Roles
	models := req.Models
	maxW := req.MaxWorkers
	preferLocal := req.PreferLocal

	if req.TemplateID != "" && r.Templates != nil {
		tpl, err := r.Templates.Get(req.TemplateID)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", req.TemplateID, err)
		}
		if !tpl.Enabled {
			return nil, fmt.Errorf("template %q disabled", req.TemplateID)
		}
		if prompt == "" {
			prompt = tpl.Prompt
		}
		if len(roles) == 0 {
			roles = tpl.Roles
		}
		if len(models) == 0 {
			models = tpl.Models
		}
		if maxW <= 0 {
			maxW = tpl.MaxWorkers
		}
		preferLocal = preferLocal || tpl.PreferLocal
	}
	if prompt == "" {
		return nil, fmt.Errorf("prompt required")
	}
	if len(roles) == 0 {
		roles = []string{string(RolePlan), string(RoleExec)}
	}
	opts := r.Opts
	if maxW > 0 {
		opts.MaxWorkers = maxW
	}
	if opts.MaxWorkers <= 0 {
		opts.MaxWorkers = 2
	}
	if opts.MaxWorkers > 4 {
		opts.MaxWorkers = 4
	}
	n := opts.MaxWorkers
	if len(roles) < n {
		n = len(roles)
	}
	turnID := strings.TrimSpace(req.TurnID)
	if turnID == "" {
		turnID = "swarm-" + uuid.NewString()
	}
	opts.TurnID = turnID

	_ = preferLocal // WorkerFn / Completer wrapper applies /local when wired.

	workers := make([]Worker, n)
	for i := 0; i < n; i++ {
		i := i
		role := Role(roles[i])
		if role == "" {
			role = RoleWorker
		}
		model := r.DefaultModel
		if len(models) > i && models[i] != "" {
			model = models[i]
		} else if len(models) == 1 && models[0] != "" {
			model = models[0]
		}
		rolePrompt := fmt.Sprintf("[%s]\n%s", role, prompt)
		workers[i] = Worker{
			ID:    fmt.Sprintf("%s-%d", role, i),
			Role:  role,
			Model: model,
			Run: func(wctx context.Context) (contextkit.Episode, error) {
				return r.WorkerFn(wctx, role, model, rolePrompt)
			},
		}
	}

	if r.Graph != nil {
		opts.OnResult = func(res Result) {
			r.Graph.AppendTurn(turnID, res.WorkerID, string(res.Role), res.Model, res.Err == nil, res.Episode.Summary)
		}
	}

	start := time.Now()
	results, err := FanOut(ctx, workers, opts)
	merged := MergeResults(results)
	merged.ID = turnID + "-merge"
	summary := OrchestratorSummary(merged, results)

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = r.SessionID
	}
	if r.Episodes != nil && sessionID != "" {
		r.Episodes.RecordEpisode(sessionID, merged)
	}

	views := make([]ResultView, len(results))
	for i, res := range results {
		views[i] = ResultView{
			WorkerID: res.WorkerID,
			Role:     string(res.Role),
			Model:    res.Model,
			Summary:  res.Episode.Summary,
			Tokens:   res.Episode.Tokens,
			Episode:  res.Episode,
		}
		if res.Err != nil {
			views[i].Err = res.Err.Error()
		}
	}

	out := &RunResponse{
		TurnID:    turnID,
		Summary:   summary,
		Episode:   merged,
		Results:   views,
		ElapsedMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		// Partial merge still returned; surface error alongside when all failed.
		ok := 0
		for _, v := range views {
			if v.Err == "" && v.Summary != "" {
				ok++
			}
		}
		if ok == 0 {
			return out, err
		}
	}
	return out, nil
}

// CompleterWorkerFn adapts an HTTP Completer-style callback for Runner.WorkerFn.
// forcePrefix is prepended when preferLocal (e.g. "/local ").
func CompleterWorkerFn(complete func(ctx context.Context, r *http.Request, prompt, model string) (string, error), preferLocal bool) WorkerFn {
	return func(ctx context.Context, role Role, model, prompt string) (contextkit.Episode, error) {
		msg := prompt
		if preferLocal && !strings.HasPrefix(strings.TrimSpace(msg), "/local") {
			msg = "/local " + msg
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://glider.local/swarm", nil)
		text, err := complete(ctx, req, msg, model)
		ep := contextkit.Episode{
			Summary: text,
			Model:   model,
			Tokens:  len(text) / 4,
			Reason:  "swarm_run",
			Role:    string(role),
		}
		return ep, err
	}
}
