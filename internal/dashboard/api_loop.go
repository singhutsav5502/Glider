package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/loop"
)

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
	case id == "clear-all-results" && r.Method == http.MethodPost:
		cleared, skipped, err := s.Loops.ClearAllResults()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.AgentLogs != nil {
			s.AgentLogs.ClearScope(agentlog.ScopeHoop)
			s.AgentLogs.ClearScope(agentlog.ScopeSwarm)
		}
		writeJSON(w, map[string]any{"cleared": cleared, "skipped": skipped})
		return
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
		hoopsDir := s.HoopsDir
		if hoopsDir == "" {
			hoopsDir = loop.DefaultHoopsDir()
		}
		_ = loop.WriteHoopYAML(hoopsDir, st.Spec)
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
	case action == "approve" && r.Method == http.MethodPost:
		dec, err := readGateDecision(r, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := s.Loops.DecideGate(id, dec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "reject" && r.Method == http.MethodPost:
		dec, err := readGateDecision(r, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := s.Loops.DecideGate(id, dec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "resume" && r.Method == http.MethodPost:
		st, err := s.Loops.Resume(context.Background(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "clear-results" && r.Method == http.MethodPost:
		st, err := s.Loops.ClearResults(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, st)
	case action == "snapshot" && r.Method == http.MethodGet:
		st, err := s.Loops.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, loop.SnapshotGraph(st.Spec))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func readGateDecision(r *http.Request, approve bool) (loop.GateDecision, error) {
	dec := loop.GateDecision{Approve: approve, Resume: approve}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return dec, err
	}
	defer r.Body.Close()
	if len(strings.TrimSpace(string(body))) == 0 {
		return dec, nil
	}
	if err := json.Unmarshal(body, &dec); err != nil {
		return dec, err
	}
	dec.Approve = approve
	if !approve {
		dec.Resume = false
	}
	return dec, nil
}
