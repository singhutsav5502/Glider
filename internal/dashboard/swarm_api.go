package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/swarm"
)

func (s *Server) handleHotSwapList(w http.ResponseWriter, r *http.Request) {
	type payload struct {
		Modules []swarm.ModuleInfo   `json:"modules"`
		Docs    map[string]string    `json:"docs"`
		Catalog []swarm.ModuleInfo   `json:"catalog"`
	}
	out := payload{
		Modules: []swarm.ModuleInfo{},
		Docs:    swarm.Docs(),
		Catalog: swarm.BuiltinCatalog(),
	}
	if s.HotSwap != nil {
		out.Modules = s.HotSwap.List()
		if out.Modules == nil {
			out.Modules = []swarm.ModuleInfo{}
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleHotSwapEnable(w http.ResponseWriter, r *http.Request) {
	if s.HotSwap == nil {
		http.Error(w, "hot-swap registry not configured", http.StatusServiceUnavailable)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/hotswap/modules/"), "/")
	if name == "" {
		http.Error(w, "missing module name", http.StatusBadRequest)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if err := s.HotSwap.SetEnabled(name, body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"name": name, "enabled": body.Enabled})
}

func (s *Server) handleSwarmRun(w http.ResponseWriter, r *http.Request) {
	if s.Swarm == nil {
		http.Error(w, "swarm not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	var req swarm.RunWavesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var resp *swarm.RunResponse
	if req.Waves > 1 || req.Decompose || req.WeavePolicy != "" || len(req.SubTasks) > 0 {
		if req.Waves <= 1 && (req.Decompose || len(req.SubTasks) > 0) {
			req.Waves = 2
		}
		resp, err = s.Swarm.RunWaves(r.Context(), req)
	} else {
		resp, err = s.Swarm.Run(r.Context(), req.RunRequest)
	}
	if err != nil && resp == nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		w.Header().Set("X-Glider-Swarm-Error", err.Error())
	}
	writeJSON(w, resp)
}

func (s *Server) handleSwarmThreads(w http.ResponseWriter, r *http.Request) {
	if s.Swarm == nil || s.Swarm.Threads == nil {
		writeJSON(w, []any{})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	list, err := s.Swarm.Threads.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []swarm.ThreadSummary{}
	}
	writeJSON(w, list)
}

func (s *Server) handleSwarmThread(w http.ResponseWriter, r *http.Request) {
	if s.Swarm == nil || s.Swarm.Threads == nil {
		http.Error(w, "threads not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/swarm/threads/"), "/")
	if path == "" {
		http.Error(w, "missing thread id", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "resume" && r.Method == http.MethodPost:
		var body struct {
			Waves       int              `json:"waves"`
			WeavePolicy swarm.WeavePolicy `json:"weave_policy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		defer r.Body.Close()
		if body.Waves <= 0 {
			body.Waves = 1
		}
		resp, err := s.Swarm.ResumeThread(r.Context(), id, body.Waves, body.WeavePolicy)
		if err != nil && resp == nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			w.Header().Set("X-Glider-Swarm-Error", err.Error())
		}
		writeJSON(w, resp)
	case action == "" && r.Method == http.MethodGet:
		st, err := s.Swarm.Threads.Load(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, st)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleContextIndexTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	g, ok := s.ContextGraph.(*contextgraph.Store)
	if !ok || g == nil {
		http.Error(w, "context graph not enabled", http.StatusNotFound)
		return
	}
	var body struct {
		TurnID   string `json:"turn_id"`
		Root     string `json:"root"`
		MaxDepth int    `json:"max_depth"`
		MaxFiles int    `json:"max_files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	n, err := g.IndexFileTree(body.TurnID, body.Root, body.MaxDepth, body.MaxFiles)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"indexed": n, "root": body.Root, "turn_id": body.TurnID})
}

func (s *Server) handleSwarmTemplates(w http.ResponseWriter, r *http.Request) {
	if s.Templates == nil {
		writeJSON(w, []any{})
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.Templates.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		var tpl swarm.Template
		if err := json.Unmarshal(body, &tpl); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tpl.Enabled = true
		if err := s.Templates.Save(&tpl); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, tpl)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSwarmTemplate(w http.ResponseWriter, r *http.Request) {
	if s.Templates == nil {
		http.Error(w, "templates not configured", http.StatusServiceUnavailable)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/swarm/templates/"), "/")
	if id == "" {
		http.Error(w, "missing template id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		tpl, err := s.Templates.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, tpl)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		var tpl swarm.Template
		if err := json.Unmarshal(body, &tpl); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tpl.ID = id
		if err := s.Templates.Save(&tpl); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, tpl)
	case http.MethodDelete:
		if err := s.Templates.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// NewGraphSwarmSink adapts a contextgraph store for swarm.Runner.Graph.
func NewGraphSwarmSink(g *contextgraph.Store) swarm.GraphSink {
	if g == nil {
		return nil
	}
	return graphSwarmSink{g: g}
}

// NewGraphContext adapts contextgraph.Store to swarm.GraphContext (dual-layer).
func NewGraphContext(g *contextgraph.Store) swarm.GraphContext {
	if g == nil {
		return nil
	}
	return graphContext{g: g}
}

// graphSwarmSink adapts contextgraph.Store to swarm.GraphSink.
type graphSwarmSink struct {
	g *contextgraph.Store
}

func (s graphSwarmSink) AppendTurn(turnID, workerID, role, model string, ok bool, summary string) {
	if s.g == nil {
		return
	}
	attrs := map[string]string{
		"worker_id":  workerID,
		"role":       role,
		"model":      model,
		"ok":         strconv.FormatBool(ok),
		"provenance": string(contextgraph.ProvenanceRuntime),
	}
	if summary != "" {
		if len(summary) > 160 {
			summary = summary[:160] + "…"
		}
		attrs["summary"] = summary
	}
	s.g.Append(contextgraph.Event{
		Kind:   contextgraph.EventSwarmFanOut,
		TurnID: turnID,
		Actor:  "swarm",
		Attrs:  attrs,
	})
	// Structural layer: worker entity + optional episode link.
	wid := turnID + "-" + workerID
	s.g.RecordFact(turnID, contextgraph.Fact{
		ID:         wid,
		Kind:       contextgraph.KindWorker,
		Label:      workerID,
		Provenance: contextgraph.ProvenanceRuntime,
		Attrs:      attrs,
	})
}

type graphContext struct {
	g *contextgraph.Store
}

func (c graphContext) Query(turnID, q string, limit int) string {
	if c.g == nil {
		return ""
	}
	return c.g.Query(turnID, q, limit)
}

func (c graphContext) PathSummary(turnID, from, to string) string {
	if c.g == nil {
		return ""
	}
	return c.g.PathSummary(turnID, from, to)
}

func (c graphContext) WaveOutputs(turnID string, waveIndex int, limit int) []string {
	if c.g == nil {
		return nil
	}
	return c.g.WaveOutputs(turnID, waveIndex, limit)
}

func (c graphContext) RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []swarm.WaveWorkerOut) {
	if c.g == nil {
		return
	}
	ws := make([]contextgraph.WaveWorker, len(workers))
	for i, w := range workers {
		ws[i] = contextgraph.WaveWorker{
			WorkerID: w.WorkerID, Role: w.Role, Model: w.Model, Summary: w.Summary, OK: w.OK,
		}
	}
	c.g.RecordThreadWave(turnID, threadID, waveIndex, mergedID, mergedSummary, ws)
}

func (c graphContext) RecordEpisodeFact(turnID, episodeID, label, summary string) {
	if c.g == nil {
		return
	}
	c.g.RecordEpisodeFact(turnID, episodeID, label, summary)
}
