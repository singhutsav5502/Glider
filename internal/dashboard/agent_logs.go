package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/agentlog"
)

// handleAgentLogs serves recent per-instance logs.
// GET /api/agent-logs?scope=hoop|swarm&id=<instance>&limit=100
// GET /api/agent-logs?scope=hoop|swarm&id=<instance>&after_seq=<n> → entries with Seq > n
//     (also accepts afterSeq). Omit after_seq for newest-limit Recent snapshot.
// GET /api/agent-logs?scope=hoop|swarm  → list instance ids for that scope.
// DELETE /api/agent-logs?scope=hoop|swarm&id=<instance> → clear one ring
// DELETE /api/agent-logs?all=1 → clear every ring
// DELETE /api/agent-logs?scope=hoop|swarm → clear that scope
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if s.AgentLogs == nil {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"entries": []any{}, "instances": []any{}})
			return
		}
		if r.Method == http.MethodDelete {
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodDelete {
		if r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true" {
			s.AgentLogs.ClearAll()
			writeJSON(w, map[string]any{"ok": true, "cleared": "all"})
			return
		}
		scope := agentlog.Scope(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))))
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if scope != agentlog.ScopeHoop && scope != agentlog.ScopeSwarm {
			http.Error(w, "scope must be hoop|swarm (or all=1)", http.StatusBadRequest)
			return
		}
		if id == "" {
			s.AgentLogs.ClearScope(scope)
			writeJSON(w, map[string]any{"ok": true, "cleared": string(scope)})
			return
		}
		s.AgentLogs.Clear(scope, id)
		writeJSON(w, map[string]any{"ok": true, "scope": scope, "instance_id": id})
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scope := agentlog.Scope(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if scope != agentlog.ScopeHoop && scope != agentlog.ScopeSwarm {
		http.Error(w, "scope must be hoop|swarm", http.StatusBadRequest)
		return
	}
	if id == "" {
		writeJSON(w, map[string]any{
			"scope":     scope,
			"instances": s.AgentLogs.ListInstances(scope),
		})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	afterRaw := strings.TrimSpace(r.URL.Query().Get("after_seq"))
	if afterRaw == "" {
		afterRaw = strings.TrimSpace(r.URL.Query().Get("afterSeq"))
	}
	var entries []agentlog.Entry
	if afterRaw != "" {
		afterSeq, err := strconv.ParseUint(afterRaw, 10, 64)
		if err != nil {
			http.Error(w, "after_seq must be a non-negative integer", http.StatusBadRequest)
			return
		}
		entries = s.AgentLogs.After(scope, id, afterSeq, limit)
	} else {
		entries = s.AgentLogs.Recent(scope, id, limit)
	}
	if entries == nil {
		entries = []agentlog.Entry{}
	}
	writeJSON(w, map[string]any{
		"scope":       scope,
		"instance_id": id,
		"entries":     entries,
	})
}
