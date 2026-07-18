package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/agentlog"
)

// handleAgentLogs serves recent per-instance logs.
// GET /api/agent-logs?scope=hoop|swarm&id=<instance>&limit=100
// Also: GET /api/agent-logs?scope=hoop|swarm  → list instance ids for that scope.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.AgentLogs == nil {
		writeJSON(w, map[string]any{"entries": []any{}, "instances": []any{}})
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
	entries := s.AgentLogs.Recent(scope, id, limit)
	if entries == nil {
		entries = []agentlog.Entry{}
	}
	writeJSON(w, map[string]any{
		"scope":       scope,
		"instance_id": id,
		"entries":     entries,
	})
}
