package swarm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/statemachine"
	"github.com/glider-ai/glider/internal/tools"
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
	// Soft/hard budgets for this run (0 = inherit Runner.Governance).
	SoftTokens int `json:"soft_tokens,omitempty"`
	HardTokens int `json:"hard_tokens,omitempty"`
	// ToolRefs optional per-worker tools (same unified registry as hoop).
	Tools []tools.Ref `json:"tools,omitempty"`
}

// DecisionRouteView is Cytoscape-friendly live route paint (parity with hoop).
type DecisionRouteView struct {
	Current       string   `json:"current,omitempty"`
	PathTaken     []string `json:"path_taken,omitempty"`
	EdgesTaken    []string `json:"edges_taken,omitempty"`
	NextEdges     []string `json:"next_edges,omitempty"`
	Topology      string   `json:"topology,omitempty"`
	RouteStatus   string   `json:"route_status,omitempty"`
	MergeFailed   bool     `json:"merge_failed,omitempty"`
	MergeNarrative string  `json:"merge_narrative,omitempty"`
}

// RunResponse is the orchestrator-facing merge result.
type RunResponse struct {
	TurnID    string              `json:"turn_id"`
	Summary   string              `json:"summary"`
	Episode   contextkit.Episode  `json:"episode"`
	Results   []ResultView        `json:"results"`
	ElapsedMS int64               `json:"elapsed_ms"`
	Progress  DecisionRouteView   `json:"progress,omitempty"`
	Tokens    int                 `json:"tokens,omitempty"`
	BudgetHit string              `json:"budget_hit,omitempty"`
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

// Governance is optional soft/hard token caps for swarm runs.
type Governance struct {
	SoftTokens int
	HardTokens int
	Denylist   []string
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
	// Logs is optional per-swarm-run agent activity (keyed by turn/run id).
	Logs *agentlog.Store
	// Tools is the shared unified registry (same instance as hoop Manager when wired).
	Tools *tools.Registry
	// Governance defaults for runs.
	Governance Governance
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

// Run executes a fan-out wave through TopologySwarm and returns a merged summary.
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

	soft := req.SoftTokens
	if soft <= 0 {
		soft = r.Governance.SoftTokens
	}
	hard := req.HardTokens
	if hard <= 0 {
		hard = r.Governance.HardTokens
	}

	// Build TopologySwarm machine for live DecisionRoute paint.
	roleSlice := roles[:n]
	def, err := statemachine.FromSwarmRoles(turnID, "1", roleSlice)
	if err != nil {
		return nil, err
	}
	rt, err := statemachine.NewRuntime(def)
	if err != nil {
		return nil, err
	}
	rt.Enter()
	progress := routeView(rt)

	if r.Logs != nil {
		r.Logs.Reset(agentlog.ScopeSwarm, turnID)
		r.Logs.Info(agentlog.ScopeSwarm, turnID, "lifecycle", "swarm run started", map[string]string{
			"workers":  fmt.Sprintf("%d", n),
			"prompt":   truncate(prompt, 80),
			"topology": string(def.Topology),
		})
	}

	_ = preferLocal

	// Optional shared tool context (denylist filtered).
	toolBlock := ""
	refs := filterSwarmTools(req.Tools, r.Governance.Denylist)
	if r.Tools != nil && len(refs) > 0 {
		results := r.Tools.InvokeAllParallel(ctx, refs, prompt)
		toolBlock = tools.FormatToolResults(results)
	}
	if r.Tools != nil {
		if cq, err := r.Tools.Invoke(ctx, tools.Ref{Name: "context_query", Kind: tools.KindBuiltin}, turnID+" "); err == nil && cq.OK && cq.Output != "" {
			toolBlock += "\n\n[shared_context]\n" + truncate(cq.Output, 1200)
		}
	}

	workers := make([]Worker, n)
	spent := 0
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
		if toolBlock != "" {
			rolePrompt += "\n\n" + toolBlock
		}
		// Advance SM path for this worker.
		workerID := fmt.Sprintf("%s-%d", role, i)
		sid := statemachine.StateID("worker-" + strings.ToLower(string(role)))
		rt.Current = sid
		rt.Path = append(rt.Path, sid)
		rt.Status = statemachine.StatusRunning
		_, _ = rt.Next()
		progress = routeView(rt)

		workers[i] = Worker{
			ID:    workerID,
			Role:  role,
			Model: model,
			Run: func(wctx context.Context) (contextkit.Episode, error) {
				if r.Logs != nil {
					r.Logs.Info(agentlog.ScopeSwarm, turnID, "worker", "worker start: "+string(role), map[string]string{
						"role": string(role), "model": model,
					})
				}
				ep, err := r.WorkerFn(wctx, role, model, rolePrompt)
				if r.Logs != nil {
					if err != nil {
						r.Logs.Error(agentlog.ScopeSwarm, turnID, "worker", "worker fail: "+string(role)+" -- "+truncate(err.Error(), 100), map[string]string{
							"role": string(role),
						})
					} else {
						r.Logs.Info(agentlog.ScopeSwarm, turnID, "worker", "worker ok: "+string(role), map[string]string{
							"role": string(role), "tokens": fmt.Sprintf("%d", ep.Tokens),
						})
					}
				}
				return ep, err
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
	for _, res := range results {
		spent += res.Episode.Tokens
	}
	budgetHit := ""
	if hard > 0 && spent >= hard {
		budgetHit = "hard_tokens"
	} else if soft > 0 && spent >= soft {
		budgetHit = "soft_tokens"
	}

	merged := CritiqueMerge(results)
	merged.ID = turnID + "-merge"
	rt.Current = "merge"
	rt.Path = append(rt.Path, "merge")
	rt.Status = statemachine.StatusCompleted
	progress = routeView(rt)

	summary := OrchestratorSummary(merged, results)
	mergeFailed := false
	failN := 0
	for _, res := range results {
		if res.Err != nil {
			failN++
		}
	}
	if failN > 0 {
		mergeFailed = failN == len(results) || strings.Contains(strings.ToLower(summary), "fail")
		progress.MergeFailed = mergeFailed
		progress.MergeNarrative = truncate(summary, 400)
		if r.Logs != nil {
			r.Logs.Info(agentlog.ScopeSwarm, turnID, "merge", "merge narrative: "+truncate(summary, 160), map[string]string{
				"failed_workers": fmt.Sprintf("%d", failN),
				"merge_failed":   fmt.Sprintf("%t", mergeFailed),
			})
		}
	}
	if r.Logs != nil {
		r.Logs.Info(agentlog.ScopeSwarm, turnID, "lifecycle", "swarm merge: "+truncate(summary, 160), map[string]string{
			"elapsed_ms": fmt.Sprintf("%d", time.Since(start).Milliseconds()),
			"path":       strings.Join(progress.PathTaken, ","),
		})
	}

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
		Progress:  progress,
		Tokens:    spent,
		BudgetHit: budgetHit,
	}
	if budgetHit == "hard_tokens" {
		return out, fmt.Errorf("budget_exceeded:hard_tokens")
	}
	if err != nil {
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

func routeView(rt *statemachine.Runtime) DecisionRouteView {
	if rt == nil {
		return DecisionRouteView{}
	}
	r := rt.Route
	path := make([]string, len(r.PathTaken))
	for i, s := range r.PathTaken {
		path[i] = string(s)
	}
	if len(path) == 0 && len(rt.Path) > 0 {
		path = make([]string, len(rt.Path))
		for i, s := range rt.Path {
			path[i] = string(s)
		}
	}
	cur := string(r.Current)
	if cur == "" {
		cur = string(rt.Current)
	}
	return DecisionRouteView{
		Current:     cur,
		PathTaken:   path,
		EdgesTaken:  append([]string(nil), r.EdgesTaken...),
		NextEdges:   append([]string(nil), r.NextEdges...),
		Topology:    string(rt.Def.Topology),
		RouteStatus: string(r.Status),
	}
}

func filterSwarmTools(refs []tools.Ref, deny []string) []tools.Ref {
	if len(deny) == 0 {
		return refs
	}
	var out []tools.Ref
	for _, r := range refs {
		hit := false
		for _, d := range deny {
			if strings.EqualFold(d, r.Name) {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, r)
		}
	}
	return out
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
