package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
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
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/mcp/github"), "/")
	switch {
	case path == "" && r.Method == http.MethodGet:
		if s.MCP == nil {
			writeJSON(w, mcp.GitHubStatus{
				TokenConfigured:  mcp.GitHubTokenPresent(),
				TokenEnv:         mcp.ResolveGitHubTokenEnv(),
				TokenSource:      mcp.GitHubTokenSource(),
				RemoteURL:        mcp.GitHubRemoteURL,
				DockerImage:      mcp.GitHubDockerImage,
				OAuthClientIDSet: mcp.ResolveGitHubOAuthClientID() != "",
				DeviceFlowReady:  mcp.ResolveGitHubOAuthClientID() != "",
				Hint:             "Glider dashboard can be up while GitHub MCP is still disconnected.",
			})
			return
		}
		writeJSON(w, s.MCP.GitHubStatusSnapshot(r.Context()))

	case path == "token" && r.Method == http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if s.MCP == nil {
			if err := mcp.SaveGitHubToken(body.Token); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "connected": false, "note": "token saved; mcp manager not wired"})
			return
		}
		if err := s.MCP.ApplyGitHubTokenAndConnect(r.Context(), body.Token); err != nil {
			// Token may still be saved; report connect error.
			_ = mcp.SaveGitHubToken(body.Token)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, s.MCP.GitHubStatusSnapshot(r.Context()))

	case path == "token" && r.Method == http.MethodDelete:
		_ = mcp.ClearGitHubToken()
		if s.MCP != nil {
			_ = s.MCP.Disconnect(r.Context(), "github")
			_ = s.MCP.Disconnect(r.Context(), "github-stdio")
		}
		writeJSON(w, map[string]any{"ok": true})

	case path == "oauth/start" && (r.Method == http.MethodPost || r.Method == http.MethodGet):
		base := "http://127.0.0.1:8081"
		if s.Config != nil {
			if cfg := s.Config.Get(); cfg != nil && cfg.Server.DashboardPort > 0 {
				base = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.DashboardPort)
			}
		}
		start, err := mcp.StartGitHubOAuthAuthorize(base)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, start)

	case path == "device/start" && r.Method == http.MethodPost:
		start, err := mcp.StartGitHubDeviceFlow(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, start)

	case path == "device/poll" && r.Method == http.MethodPost:
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		poll, err := mcp.PollGitHubDeviceFlow(r.Context(), body.DeviceCode)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if poll.Status == "authorized" {
			if s.MCP != nil {
				if err := s.MCP.ApplyGitHubTokenAndConnect(r.Context(), poll.AccessToken); err != nil {
					_ = mcp.SaveGitHubToken(poll.AccessToken)
					http.Error(w, "authorized but connect failed: "+err.Error(), http.StatusBadRequest)
					return
				}
				snap := s.MCP.GitHubStatusSnapshot(r.Context())
				poll.Connected = snap.HTTPConnected
				poll.HTTPOK = snap.HTTPConnected
			} else {
				_ = mcp.SaveGitHubToken(poll.AccessToken)
			}
			poll.AccessToken = "" // never echo secret
		}
		writeJSON(w, poll)

	default:
		if path == "" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleGitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if errStr := q.Get("error"); errStr != "" {
		http.Error(w, errStr+": "+q.Get("error_description"), http.StatusBadRequest)
		return
	}
	base := "http://127.0.0.1:8081"
	if s.Config != nil {
		if cfg := s.Config.Get(); cfg != nil && cfg.Server.DashboardPort > 0 {
			base = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.DashboardPort)
		}
	}
	tok, err := mcp.ExchangeGitHubOAuthCode(r.Context(), q.Get("code"), q.Get("state"), base)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.MCP != nil {
		if err := s.MCP.ApplyGitHubTokenAndConnect(r.Context(), tok); err != nil {
			_ = mcp.SaveGitHubToken(tok)
			http.Error(w, "token saved but MCP connect failed: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		_ = mcp.SaveGitHubToken(tok)
	}
	http.Redirect(w, r, "/?tab=mcp&github=connected", http.StatusFound)
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
		res, err := s.MCP.ListToolsOrCatalogResult(r.Context(), id)
		if err != nil {
			writeMCPErr(w, err)
			return
		}
		if res.Tools == nil {
			res.Tools = []mcp.Tool{}
		}
		writeJSON(w, map[string]any{
			"server_id":    id,
			"source":       res.Source,
			"tools":        res.Tools,
			"health_error": res.HealthError,
			"message":      res.Message,
		})

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
