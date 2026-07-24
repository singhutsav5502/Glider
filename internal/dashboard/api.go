package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
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
		Enabled      bool                    `json:"enabled"`
		DumpDir      string                  `json:"dump_dir,omitempty"`
		Recent       []mitm.RPCObservation   `json:"recent"`
		PathCounts   map[string]int          `json:"path_counts"`
		Metrics      map[string]int          `json:"metrics,omitempty"`
		Distribution *metrics.Distribution   `json:"distribution,omitempty"`
		ContextTurns []contextgraph.TurnView `json:"context_turns,omitempty"`
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
