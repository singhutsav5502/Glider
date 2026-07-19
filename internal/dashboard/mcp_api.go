package dashboard

import (
	"errors"
	"net/http"
	"strings"

	"github.com/glider-ai/glider/internal/mcp"
)

func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.MCP == nil {
		writeJSON(w, map[string]any{
			"servers": []any{},
			"github": mcp.GitHubStatus{
				TokenConfigured: mcp.GitHubTokenPresent(),
				TokenEnv:        mcp.ResolveGitHubTokenEnv(),
				RemoteURL:       mcp.GitHubRemoteURL,
				DockerImage:     mcp.GitHubDockerImage,
			},
		})
		return
	}
	writeJSON(w, map[string]any{
		"servers": s.MCP.Status(r.Context()),
		"github":  s.MCP.GitHubStatusSnapshot(r.Context()),
	})
}

func (s *Server) handleMCPGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.MCP == nil {
		writeJSON(w, mcp.GitHubStatus{
			TokenConfigured: mcp.GitHubTokenPresent(),
			TokenEnv:        mcp.ResolveGitHubTokenEnv(),
			RemoteURL:       mcp.GitHubRemoteURL,
			DockerImage:     mcp.GitHubDockerImage,
		})
		return
	}
	writeJSON(w, s.MCP.GitHubStatusSnapshot(r.Context()))
}

// handleMCPServer routes /api/mcp/servers/{id} and /api/mcp/servers/{id}/{action}.
func (s *Server) handleMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		http.Error(w, "mcp manager not configured", http.StatusServiceUnavailable)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/mcp/servers/"), "/")
	if rest == "" {
		http.Error(w, "missing server id", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		st, ok := s.MCP.StatusOf(r.Context(), id)
		if !ok {
			http.Error(w, "server not found", http.StatusNotFound)
			return
		}
		writeJSON(w, st)

	case action == "tools" && r.Method == http.MethodGet:
		tools, source, err := s.MCP.ListToolsOrCatalog(r.Context(), id)
		if err != nil {
			writeMCPErr(w, err)
			return
		}
		if tools == nil {
			tools = []mcp.Tool{}
		}
		writeJSON(w, map[string]any{"server_id": id, "source": source, "tools": tools})

	case action == "connect" && r.Method == http.MethodPost:
		if err := s.MCP.ConnectConfigured(r.Context(), id); err != nil {
			writeMCPErr(w, err)
			return
		}
		st, _ := s.MCP.StatusOf(r.Context(), id)
		writeJSON(w, st)

	case action == "disconnect" && r.Method == http.MethodPost:
		if err := s.MCP.Disconnect(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, ok := s.MCP.StatusOf(r.Context(), id)
		if !ok {
			writeJSON(w, map[string]any{"id": id, "connected": false})
			return
		}
		writeJSON(w, st)

	case action == "reconnect" && r.Method == http.MethodPost:
		if err := s.MCP.Reconnect(r.Context(), id); err != nil {
			writeMCPErr(w, err)
			return
		}
		st, _ := s.MCP.StatusOf(r.Context(), id)
		writeJSON(w, st)

	case action == "refresh" && r.Method == http.MethodPost:
		// Alias: refresh = reconnect when connected, else connect.
		if err := s.MCP.Health(r.Context(), id); err == nil {
			if err := s.MCP.Reconnect(r.Context(), id); err != nil {
				writeMCPErr(w, err)
				return
			}
		} else {
			if err := s.MCP.ConnectConfigured(r.Context(), id); err != nil {
				writeMCPErr(w, err)
				return
			}
		}
		st, _ := s.MCP.StatusOf(r.Context(), id)
		writeJSON(w, st)

	default:
		if action == "" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

func writeMCPErr(w http.ResponseWriter, err error) {
	var nc *mcp.NotConfiguredError
	if errors.As(err, &nc) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}
