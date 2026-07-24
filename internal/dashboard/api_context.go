package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
)

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
		Turns        []contextgraph.TurnView `json:"turns"`
		RecentEvents []contextgraph.Event    `json:"recent_events"`
		Stats        contextgraph.StoreStats `json:"stats"`
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
		Turns []contextgraph.TurnView `json:"turns"`
		Stats contextgraph.StoreStats `json:"stats"`
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
		Session  string               `json:"session,omitempty"`
		Episodes []contextkit.Episode `json:"episodes"`
		Sessions []string             `json:"sessions,omitempty"`
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
