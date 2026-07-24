package swarm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/statemachine"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/google/uuid"
)

// WorkerFn executes one role/model prompt and returns an Episode.
type WorkerFn func(ctx context.Context, role Role, model, prompt string) (contextkit.Episode, error)

// CriticFn is an optional LLM completer used by weave policy llm_critic.
type CriticFn func(ctx context.Context, prompt, model string) (string, error)

// RunRequest is the POST /api/swarm/run body.
type RunRequest struct {
	Prompt      string   `json:"prompt"`
	Roles       []string `json:"roles,omitempty"`
	Models      []string `json:"models,omitempty"`
	MaxWorkers  int      `json:"max_workers,omitempty"`
	TurnID      string   `json:"turn_id,omitempty"`
	TemplateID  string   `json:"template_id,omitempty"`
	PreferLocal bool     `json:"prefer_local,omitempty"`
	// Route selects inference backend: local (Ollama/vLLM), cloud (BYOK), or auto (gateway rules).
	// When empty, PreferLocal true → local; otherwise auto.
	Route     string `json:"route,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Soft/hard budgets for this run (0 = inherit Runner.Governance).
	SoftTokens int `json:"soft_tokens,omitempty"`
	HardTokens int `json:"hard_tokens,omitempty"`
	// ToolRefs optional per-worker tools (same unified registry as hoop).
	Tools []tools.Ref `json:"tools,omitempty"`
	// FreeSpawn builds workers from SubTasks (role invent); capped by MaxWorkers.
	FreeSpawn bool `json:"free_spawn,omitempty"`
	// SubTasks optional per-worker prompts/roles when FreeSpawn is set.
	SubTasks []backend.SubTask `json:"subtasks,omitempty"`
}

// DecisionRouteView is Cytoscape-friendly live route paint (parity with hoop).
type DecisionRouteView struct {
	Current        string   `json:"current,omitempty"`
	PathTaken      []string `json:"path_taken,omitempty"`
	EdgesTaken     []string `json:"edges_taken,omitempty"`
	NextEdges      []string `json:"next_edges,omitempty"`
	Topology       string   `json:"topology,omitempty"`
	RouteStatus    string   `json:"route_status,omitempty"`
	MergeFailed    bool     `json:"merge_failed,omitempty"`
	MergeNarrative string   `json:"merge_narrative,omitempty"`
}

// RunResponse is the orchestrator-facing merge result.
type RunResponse struct {
	TurnID    string             `json:"turn_id"`
	Summary   string             `json:"summary"`
	Episode   contextkit.Episode `json:"episode"`
	Results   []ResultView       `json:"results"`
	ElapsedMS int64              `json:"elapsed_ms"`
	Progress  DecisionRouteView  `json:"progress,omitempty"`
	Tokens    int                `json:"tokens,omitempty"`
	BudgetHit string             `json:"budget_hit,omitempty"`
	ThreadID  string             `json:"thread_id,omitempty"`
	Waves     int                `json:"waves,omitempty"`
	Policy    string             `json:"weave_policy,omitempty"`
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
	GraphCtx     GraphContext // dual-layer context for multi-wave seed + facts
	Threads      *ThreadStore // durable thread/wave state on disk
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
	// CriticFn optional LLM pass for WeaveLLMCritic (wired from PipelineCompleter).
	CriticFn CriticFn
	// CriticModel optional model id for CriticFn (empty → DefaultModel).
	CriticModel string
	// runRoute is the inference route for the in-flight Run / RunWaves (local|cloud|auto).
	runRoute atomic.Value // string
	// liveRuns holds mid-run fan-out snapshots for GET /api/swarm/runs/{id}/progress.
	liveMu   sync.Mutex
	liveRuns *liveStore
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

func (r *Runner) setRunRoute(route string) {
	if r == nil {
		return
	}
	r.runRoute.Store(ResolveInferenceRoute(route, false))
}

func (r *Runner) currentRunRoute() string {
	if r == nil {
		return "auto"
	}
	if v, ok := r.runRoute.Load().(string); ok && v != "" {
		return v
	}
	return "auto"
}

// ResolveInferenceRoute maps route / prefer_local to local|cloud|auto.
func ResolveInferenceRoute(route string, preferLocal bool) string {
	switch strings.ToLower(strings.TrimSpace(route)) {
	case "local", "cloud", "auto":
		return strings.ToLower(strings.TrimSpace(route))
	}
	if preferLocal {
		return "local"
	}
	return "auto"
}

// ApplyInferenceRoute prefixes /local or /cloud so the gateway router picks BYOK vs Ollama/vLLM.
func ApplyInferenceRoute(msg, route string) string {
	msg = strings.TrimSpace(msg)
	switch ResolveInferenceRoute(route, false) {
	case "local":
		if strings.HasPrefix(msg, "/local") || strings.HasPrefix(msg, "/cloud") {
			return msg
		}
		return "/local " + msg
	case "cloud":
		if strings.HasPrefix(msg, "/local") || strings.HasPrefix(msg, "/cloud") {
			return msg
		}
		return "/cloud " + msg
	default:
		return msg
	}
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
	route := strings.TrimSpace(req.Route)

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
		if route == "" && tpl.PreferLocal {
			route = "local"
		}
		if len(req.Tools) == 0 && len(tpl.Tools) > 0 {
			req.Tools = append([]tools.Ref(nil), tpl.Tools...)
		}
	}
	route = ResolveInferenceRoute(route, preferLocal)
	r.setRunRoute(route)
	if prompt == "" {
		return nil, fmt.Errorf("prompt required")
	}
	freeSpawn := req.FreeSpawn && len(req.SubTasks) > 0
	if freeSpawn {
		roles = RolesFromSubTasks(req.SubTasks, maxW)
		if len(req.Models) == 0 {
			models = ModelsFromSubTasks(req.SubTasks, maxW)
		}
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
	if freeSpawn && len(req.SubTasks) < n {
		n = len(req.SubTasks)
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
			"route":    route,
		})
	}

	liveWorkers := make([]LiveWorkerView, n)
	for i := 0; i < n; i++ {
		role := Role(roles[i])
		if role == "" {
			role = RoleWorker
		}
		liveWorkers[i] = LiveWorkerView{
			WorkerID: fmt.Sprintf("%s-%d", role, i),
			Role:     string(role),
			Status:   "pending",
		}
	}
	r.beginLive(turnID, turnID, liveWorkers, 1)
	r.setLiveProgress(turnID, progress, "fanout")

	var runLay tools.RunLayout
	if r.Tools != nil {
		var layErr error
		runLay, layErr = r.Tools.EnsureRunLayout(turnID)
		if layErr != nil && r.Logs != nil {
			r.Logs.Error(agentlog.ScopeSwarm, turnID, "artifacts", "ensure run layout: "+layErr.Error(), nil)
		} else {
			prompt = prompt + "\n\n" + runLay.PromptHint()
		}
	}

	// Optional shared tool context (denylist filtered) + default artifact I/O when tools are declared.
	toolBlock := ""
	refs := filterSwarmTools(req.Tools, r.Governance.Denylist)
	if len(refs) > 0 {
		refs = filterSwarmTools(withDefaultArtifactTools(refs), r.Governance.Denylist)
	}
	if r.Tools != nil && len(refs) > 0 {
		refs = r.Tools.ExpandRefs(ctx, refs)
		blindRefs := tools.FilterBlindSafe(refs)
		if len(blindRefs) > 0 {
			results := r.Tools.InvokeAllParallel(ctx, blindRefs, tools.BlindPrepassInput())
			toolBlock = tools.FormatToolResults(results)
		}
		// Hint remaining tools so workers know what the agentic path would use.
		if len(blindRefs) < len(refs) {
			var names []string
			for _, ref := range refs {
				if !tools.BlindSafe(ref) {
					names = append(names, ref.Name)
				}
			}
			if len(names) > 0 {
				toolBlock += "\n\n[tools_available_for_agent_loop]\n" + strings.Join(names, ", ") +
					"\n(Use structured tool_calls; not pre-invoked with the free-form prompt.)\n"
			}
		}
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
		if freeSpawn && i < len(req.SubTasks) {
			rolePrompt = fmt.Sprintf("[%s]\n%s", role, FormatSubTaskPrompt(prompt, i, req.SubTasks[i]))
			if req.SubTasks[i].Model != "" && (len(models) <= i || models[i] == "") {
				model = req.SubTasks[i].Model
			}
		}
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
				ep, err := r.WorkerFn(wctx, role, model, ApplyInferenceRoute(rolePrompt, route))
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

	prevOnResult := opts.OnResult
	opts.OnStart = func(res Result) {
		r.setLiveWorker(turnID, res.WorkerID, string(res.Role), "running", "", "")
		r.setLiveProgress(turnID, progress, "fanout")
	}
	opts.OnResult = func(res Result) {
		st := "ok"
		errMsg := ""
		if res.Err != nil {
			st = "fail"
			errMsg = res.Err.Error()
		}
		r.setLiveWorker(turnID, res.WorkerID, string(res.Role), st, res.Episode.Summary, errMsg)
		if r.Graph != nil {
			r.Graph.AppendTurn(turnID, res.WorkerID, string(res.Role), res.Model, res.Err == nil, res.Episode.Summary)
		}
		if prevOnResult != nil {
			prevOnResult(res)
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
	r.setLiveProgress(turnID, progress, "merge")
	okLive := err == nil && !mergeFailed && budgetHit != "hard_tokens"
	r.finishLive(turnID, okLive, progress)
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

func withDefaultArtifactTools(refs []tools.Ref) []tools.Ref {
	seen := map[string]bool{}
	out := make([]tools.Ref, 0, len(refs)+4)
	for _, r := range refs {
		seen[r.Name] = true
		out = append(out, r)
	}
	for _, name := range []string{"fs_read", "fs_write", "fs_list", "artifact_write"} {
		if seen[name] {
			continue
		}
		out = append(out, tools.Ref{Name: name, Kind: tools.KindBuiltin})
	}
	return out
}

// CompleterWorkerFn adapts an HTTP Completer-style callback for Runner.WorkerFn.
// Route prefixes (/local|/cloud) are applied by Runner.Run via ApplyInferenceRoute.
func CompleterWorkerFn(complete func(ctx context.Context, r *http.Request, prompt, model string) (string, error), _ bool) WorkerFn {
	return func(ctx context.Context, role Role, model, prompt string) (contextkit.Episode, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://glider.local/swarm", nil)
		text, err := complete(ctx, req, prompt, model)
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

// CompleterCriticFn adapts the same Completer callback for Runner.CriticFn.
// Callers should pass prompts already routed, or wrap with ApplyInferenceRoute.
func CompleterCriticFn(complete func(ctx context.Context, r *http.Request, prompt, model string) (string, error), _ bool) CriticFn {
	return func(ctx context.Context, prompt, model string) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://glider.local/swarm-critic", nil)
		return complete(ctx, req, prompt, model)
	}
}
