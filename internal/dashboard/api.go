package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/loop"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"gopkg.in/yaml.v3"
)

type ConfigStore interface {
	Get() *config.Config
	Update(cfg *config.Config) error
}

type ModelController interface {
	ListModels() []*backend.ModelInfo
	LoadModel(name string) error
	UnloadModel(name string) error
}

type FileConfigStore struct {
	Provider *config.Provider
	Path     string
}

func (s *FileConfigStore) Get() *config.Config { return s.Provider.Get() }

// Update validates via the same ParseConfig path as file load/hot-reload,
// persists YAML to Path, then Swap() so Watch subscribers rebuild router/aliases.
func (s *FileConfigStore) Update(cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	parsed, err := config.ParseConfig(data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.Path, data, 0o644); err != nil {
		return err
	}
	s.Provider.Swap(parsed)
	return nil
}

type RegistryModelController struct {
	Registry *backend.Registry
	OnLoad   func(name string) error
	OnUnload func(name string) error
}

func (c *RegistryModelController) ListModels() []*backend.ModelInfo {
	return c.Registry.ListModels()
}

func (c *RegistryModelController) LoadModel(name string) error {
	if c.OnLoad != nil {
		if err := c.OnLoad(name); err != nil {
			return err
		}
	}
	return c.Registry.SetModelState(name, backend.ModelStateWarm)
}

func (c *RegistryModelController) UnloadModel(name string) error {
	if c.OnUnload != nil {
		if err := c.OnUnload(name); err != nil {
			return err
		}
	}
	return c.Registry.SetModelState(name, backend.ModelStateCold)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	cfg := s.Config.Get()
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "yaml" || format == "yml" || strings.Contains(r.Header.Get("Accept"), "yaml") {
		data, err := yaml.Marshal(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	writeJSON(w, cfg)
}

// handlePutConfig replaces the full running config.
// Accepts application/json (full Config object) or YAML (application/yaml, text/yaml, application/x-yaml).
// Validation uses config.ParseConfig — the same path as startup and file hot-reload.
// When backends are reachable, unknown local model IDs are rejected (soft catalog warnings still returned via GET /api/validate).
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	cfg, err := parseConfigBody(r.Header.Get("Content-Type"), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, catalog, _, _ := s.discover(ctx)
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	// Soft catalog: warn in validate endpoint; hard-fail only structural + bad GPU indices.
	res := config.ValidateDetailed(cfg, config.ValidateOptions{
		Catalog:  catalog,
		GPUCount: gpuCount,
		Soft:     true,
	})
	if err := res.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.Config.Update(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(res.Warnings) > 0 {
		w.Header().Set("X-Glider-Warnings", strings.Join(res.Warnings, " | "))
	}
	writeJSON(w, s.Config.Get())
}

func parseConfigBody(contentType string, body []byte) (*config.Config, error) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "yaml"), strings.Contains(ct, "yml"):
		return config.ParseConfig(body)
	default:
		var raw any
		if err := json.Unmarshal(body, &raw); err != nil {
			if cfg, yerr := config.ParseConfig(body); yerr == nil {
				return cfg, nil
			}
			return nil, err
		}
		yml, err := yaml.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return config.ParseConfig(yml)
	}
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	cfg := s.Config.Get()
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		parsed, err := parseConfigBody(r.Header.Get("Content-Type"), body)
		if err != nil {
			writeJSON(w, config.ValidationResult{Errors: []string{err.Error()}})
			return
		}
		cfg = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, catalog, backendErrs, backendWarns := s.discover(ctx)
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	res := config.ValidateDetailed(cfg, config.ValidateOptions{
		Catalog:  catalog,
		GPUCount: gpuCount,
		Soft:     true,
	})
	res.Warnings = append(res.Warnings, backendWarns...)
	res.Warnings = append(res.Warnings, backendErrs...)
	writeJSON(w, res)
}

func (s *Server) handleGetModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, _, errs, warns := s.discover(ctx)
	if len(models) == 0 && s.Models != nil {
		// Fallback: registry-only list for older clients.
		writeJSON(w, s.Models.ListModels())
		return
	}
	type wrap struct {
		Models   []DiscoveredModel `json:"models"`
		Errors   []string          `json:"errors,omitempty"`
		Warnings []string          `json:"warnings,omitempty"`
	}
	// Keep backward compatible: if Accept wants bare array historically tests decode []ModelInfo.
	// Tests expect []backend.ModelInfo from registry. Prefer discovered when rich=1 or always return discovered as primary.
	if r.URL.Query().Get("rich") == "1" || r.URL.Query().Get("format") == "rich" {
		writeJSON(w, wrap{Models: models, Errors: errs, Warnings: warns})
		return
	}
	if s.Models != nil && len(models) == 0 {
		writeJSON(w, s.Models.ListModels())
		return
	}
	// Default: discovered list (array) — includes config + backend tags.
	writeJSON(w, models)
}

func (s *Server) handleGetVRAM(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, catalog, errs, warns := s.discover(ctx)
	cfg := &config.Config{}
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	snap := VRAMSnapshot{
		GPUs:            collectGPUStatus(s.GPUs),
		Models:          models,
		GPUAssignments:  map[string]int{},
		BackendErrors:   errs,
		BackendWarnings: warns,
		Catalog:         catalog.Names(),
	}
	if cfg != nil {
		snap.Strategy = cfg.VRAM.Strategy
		snap.HeadroomMB = cfg.VRAM.HeadroomMB
		snap.MaxLoadedModels = cfg.VRAM.MaxLoadedModels
		if cfg.VRAM.GPUAssignments != nil {
			snap.GPUAssignments = cfg.VRAM.GPUAssignments
		}
	}
	writeJSON(w, snap)
}

// handlePatchGPUAssignments updates vram.gpu_assignments and persists config.
func (s *Server) handlePatchGPUAssignments(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	var body struct {
		Assignments map[string]int `json:"assignments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	live := s.Config.Get()
	cfg := *live
	cfg.VRAM.GPUAssignments = map[string]int{}
	for k, v := range live.VRAM.GPUAssignments {
		cfg.VRAM.GPUAssignments[k] = v
	}
	for k, v := range body.Assignments {
		if v < 0 {
			delete(cfg.VRAM.GPUAssignments, k)
			continue
		}
		cfg.VRAM.GPUAssignments[k] = v
	}
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	res := config.ValidateDetailed(&cfg, config.ValidateOptions{GPUCount: gpuCount, Soft: true})
	if err := res.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Config.Update(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.Config.Get().VRAM.GPUAssignments)
}

func (s *Server) handleModelAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/models/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || s.Models == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := parts[0]
	action := parts[1]
	var err error
	switch action {
	case "load":
		err = s.Models.LoadModel(name)
	case "unload":
		err = s.Models.UnloadModel(name)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, []any{})
		return
	}
	sessions, err := s.History.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		http.Error(w, "history unavailable", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "requests" {
		limit := 200
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		reqs, err := s.History.ListRequests(id, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, reqs)
		return
	}
	agg, err := s.History.Aggregates(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, agg)
}

func (s *Server) handleMITMDebugRecent(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
			if limit > 500 {
				limit = 500
			}
		}
	}
	type payload struct {
		Enabled      bool                     `json:"enabled"`
		DumpDir      string                   `json:"dump_dir,omitempty"`
		Recent       []mitm.RPCObservation    `json:"recent"`
		PathCounts   map[string]int           `json:"path_counts"`
		Metrics      map[string]int           `json:"metrics,omitempty"`
		Distribution *metrics.Distribution    `json:"distribution,omitempty"`
		ContextTurns []contextgraph.TurnView  `json:"context_turns,omitempty"`
	}
	out := payload{
		Enabled:    false,
		Recent:     []mitm.RPCObservation{},
		PathCounts: map[string]int{},
	}
	if s.MITMDebug != nil {
		out.Enabled = true
		out.Recent = s.MITMDebug.Recent(limit)
		if out.Recent == nil {
			out.Recent = []mitm.RPCObservation{}
		}
		out.PathCounts = s.MITMDebug.PathCounts()
		if out.PathCounts == nil {
			out.PathCounts = map[string]int{}
		}
		if dbg, ok := s.MITMDebug.(*mitm.AgentRPCDebugger); ok && dbg != nil {
			out.DumpDir = dbg.DumpDir
			if out.DumpDir == "" {
				out.DumpDir = mitm.DefaultDebugDumpDir()
			} else {
				out.DumpDir = mitm.ExpandPath(out.DumpDir)
			}
		}
	}
	if s.Metrics != nil {
		// Expose mitm skip/intercept counters for Path B R&D.
		all := s.Metrics.GetRouteCounts()
		out.Metrics = make(map[string]int)
		for k, v := range all {
			if strings.HasPrefix(k, "action:") || strings.HasPrefix(k, "mode:mitm") {
				out.Metrics[k] = v
			}
		}
		dist := s.Metrics.GetDistribution()
		out.Distribution = &dist
	}
	if s.ContextGraph != nil {
		out.ContextTurns = s.ContextGraph.RecentTurns(10)
	}
	writeJSON(w, out)
}

func (s *Server) handleContextRecent(w http.ResponseWriter, r *http.Request) {
	turnLimit := 20
	eventLimit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			turnLimit = n
			if turnLimit > 100 {
				turnLimit = 100
			}
		}
	}
	if q := r.URL.Query().Get("events"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			eventLimit = n
			if eventLimit > 500 {
				eventLimit = 500
			}
		}
	}
	type payload struct {
		Turns         []contextgraph.TurnView `json:"turns"`
		RecentEvents  []contextgraph.Event    `json:"recent_events"`
		Stats         contextgraph.StoreStats `json:"stats"`
	}
	out := payload{
		Turns:        []contextgraph.TurnView{},
		RecentEvents: []contextgraph.Event{},
		Stats:        contextgraph.StoreStats{ByKind: map[string]int{}},
	}
	if s.ContextGraph != nil {
		out.Turns = s.ContextGraph.RecentTurns(turnLimit)
		if out.Turns == nil {
			out.Turns = []contextgraph.TurnView{}
		}
		out.RecentEvents = s.ContextGraph.RecentEvents(eventLimit)
		if out.RecentEvents == nil {
			out.RecentEvents = []contextgraph.Event{}
		}
		out.Stats = s.ContextGraph.Stats()
		if out.Stats.ByKind == nil {
			out.Stats.ByKind = map[string]int{}
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleContextTurns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
			if limit > 100 {
				limit = 100
			}
		}
	}
	type payload struct {
		Turns []contextgraph.TurnView  `json:"turns"`
		Stats contextgraph.StoreStats  `json:"stats"`
	}
	out := payload{
		Turns: []contextgraph.TurnView{},
		Stats: contextgraph.StoreStats{ByKind: map[string]int{}},
	}
	if s.ContextGraph != nil {
		out.Turns = s.ContextGraph.RecentTurns(limit)
		if out.Turns == nil {
			out.Turns = []contextgraph.TurnView{}
		}
		out.Stats = s.ContextGraph.Stats()
		if out.Stats.ByKind == nil {
			out.Stats.ByKind = map[string]int{}
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleContextTurn(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/context/turns/")
	id = strings.Trim(id, "/")
	if id == "" {
		http.Error(w, "missing turn id", http.StatusBadRequest)
		return
	}
	if s.ContextGraph == nil {
		http.Error(w, "context graph not enabled", http.StatusNotFound)
		return
	}
	view, ok := s.ContextGraph.Turn(id)
	if !ok {
		http.Error(w, "turn not found", http.StatusNotFound)
		return
	}
	writeJSON(w, view)
}

func (s *Server) handleContextEpisodes(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	limit := 32
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
			if limit > 200 {
				limit = 200
			}
		}
	}
	type payload struct {
		Session  string                `json:"session,omitempty"`
		Episodes []contextkit.Episode  `json:"episodes"`
		Sessions []string              `json:"sessions,omitempty"`
	}
	out := payload{Session: sessionID, Episodes: []contextkit.Episode{}}
	if s.Episodes == nil {
		writeJSON(w, out)
		return
	}
	if sessionID == "" {
		out.Sessions = s.Episodes.SessionIDs()
		// Merge recent across sessions (newest per session, capped).
		for _, id := range out.Sessions {
			eps := s.Episodes.RecentEpisodes(id, limit)
			out.Episodes = append(out.Episodes, eps...)
		}
		if len(out.Episodes) > limit {
			out.Episodes = out.Episodes[len(out.Episodes)-limit:]
		}
		writeJSON(w, out)
		return
	}
	out.Episodes = s.Episodes.RecentEpisodes(sessionID, limit)
	if out.Episodes == nil {
		out.Episodes = []contextkit.Episode{}
	}
	writeJSON(w, out)
}

func (s *Server) handleContextExport(w http.ResponseWriter, r *http.Request) {
	turnID := strings.TrimSpace(r.URL.Query().Get("turn"))
	maxEvents := 500
	if q := r.URL.Query().Get("events"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			maxEvents = n
		}
	}
	out := map[string]any{}
	if g, ok := s.ContextGraph.(*contextgraph.Store); ok && g != nil {
		out = g.Export(turnID, maxEvents)
	} else if s.ContextGraph != nil {
		out["turns"] = s.ContextGraph.RecentTurns(50)
		out["events"] = s.ContextGraph.RecentEvents(maxEvents)
		out["stats"] = s.ContextGraph.Stats()
	}
	if s.Episodes != nil {
		if sess := strings.TrimSpace(r.URL.Query().Get("session")); sess != "" {
			st := s.Episodes.Get(sess)
			out["session"] = st
		} else {
			out["episodes"] = s.Episodes.Export()
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleContextPrune(w http.ResponseWriter, r *http.Request) {
	retain := s.ContextRetainDays
	if retain <= 0 {
		retain = 14
	}
	if q := r.URL.Query().Get("retain_days"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			retain = n
		}
	}
	result := map[string]any{"retain_days": retain}
	if g, ok := s.ContextGraph.(*contextgraph.Store); ok && g != nil {
		n, err := g.PruneDisk(retain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result["disk_files_removed"] = n
		result["memory_events_dropped"] = g.PruneMemory()
	}
	if s.Episodes != nil {
		result["sessions_pruned"] = s.Episodes.Prune()
	}
	writeJSON(w, result)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	var snap metrics.Snapshot
	if s.Metrics == nil {
		snap = metrics.Snapshot{
			Distribution: metrics.ComputeDistribution(nil),
			RouteCounts:  map[string]int{},
		}
	} else {
		snap = s.Metrics.GetSnapshot()
	}
	// Optional companion: contextgraph turn-route tallies (does not replace distribution %).
	if s.ContextGraph != nil {
		if rt, ok := s.ContextGraph.(interface{ RouteTallies() map[string]int }); ok {
			if m := rt.RouteTallies(); len(m) > 0 {
				snap.ContextRoutes = m
			}
		}
	}
	writeJSON(w, snap)
}

func (s *Server) handleListLoops(w http.ResponseWriter, r *http.Request) {
	if s.Loops == nil {
		writeJSON(w, []any{})
		return
	}
	list, err := s.Loops.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

func (s *Server) handleCreateLoop(w http.ResponseWriter, r *http.Request) {
	if s.Loops == nil {
		http.Error(w, "loops not enabled", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var spec loop.LoopSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := s.Loops.Create(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hoopsDir := s.HoopsDir
	if hoopsDir == "" {
		hoopsDir = loop.DefaultHoopsDir()
	}
	_ = loop.WriteHoopYAML(hoopsDir, st.Spec)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, st)
}

func (s *Server) handleLoopAction(w http.ResponseWriter, r *http.Request) {
	if s.Loops == nil {
		http.Error(w, "loops not enabled", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/loops/"), "/")
	if path == "" {
		http.Error(w, "missing loop id", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		st, err := s.Loops.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, st)
	case action == "" && r.Method == http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		var spec loop.LoopSpec
		if err := json.Unmarshal(body, &spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := s.Loops.Update(id, spec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "" && r.Method == http.MethodDelete:
		if err := s.Loops.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hoopsDir := s.HoopsDir
		if hoopsDir == "" {
			hoopsDir = loop.DefaultHoopsDir()
		}
		_ = loop.DeleteHoopYAML(hoopsDir, id)
		w.WriteHeader(http.StatusNoContent)
	case action == "start" && r.Method == http.MethodPost:
		// Detach from request context — it cancels when the HTTP handler returns.
		// Manager.Stop / process Shutdown own cancellation.
		st, err := s.Loops.Start(context.Background(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "stop" && r.Method == http.MethodPost:
		if err := s.Loops.Stop(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := s.Loops.Get(id)
		if err != nil {
			writeJSON(w, map[string]string{"id": id, "status": "stopped"})
			return
		}
		writeJSON(w, st)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) discover(ctx context.Context) ([]DiscoveredModel, config.ModelCatalog, []string, []string) {
	var cfg *config.Config
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	var reg *backend.Registry
	if rc, ok := s.Models.(*RegistryModelController); ok && rc != nil {
		reg = rc.Registry
	}
	return DiscoverModels(ctx, cfg, reg)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}