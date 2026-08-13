package mcp

import (
	"context"
	"os"
	"sort"
	"strings"
)

// ServerStatus is a dashboard-facing snapshot of one configured MCP server.
type ServerStatus struct {
	ID              string        `json:"id"`
	Name            string        `json:"name,omitempty"`
	Transport       TransportKind `json:"transport,omitempty"`
	URL             string        `json:"url,omitempty"`
	Command         string        `json:"command,omitempty"`
	Connected       bool          `json:"connected"`
	HealthOK        bool          `json:"health_ok"`
	HealthError     string        `json:"health_error,omitempty"`
	ToolCount       int           `json:"tool_count"`
	Enabled         bool          `json:"enabled"`
	TokenEnv        string        `json:"token_env,omitempty"`
	TokenConfigured bool          `json:"token_configured"`
	IsGitHub        bool          `json:"is_github"`
	Toolsets        []string      `json:"toolsets,omitempty"`
}

// GitHubStatus summarizes GitHub MCP auth + connection for the dashboard.
type GitHubStatus struct {
	TokenConfigured   bool   `json:"token_configured"`
	TokenEnv          string `json:"token_env,omitempty"` // which env name resolved (never the secret)
	TokenSource       string `json:"token_source,omitempty"` // env:... | credentials_file
	HTTPConnected     bool   `json:"http_connected"`
	StdioConnected    bool   `json:"stdio_connected"`
	HTTPHealthError   string `json:"http_health_error,omitempty"`
	StdioHealthError  string `json:"stdio_health_error,omitempty"`
	RemoteURL         string `json:"remote_url"`
	DockerImage       string `json:"docker_image"`
	OAuthClientIDSet  bool   `json:"oauth_client_id_set"`
	OAuthSecretSet    bool   `json:"oauth_secret_set"`
	DeviceFlowReady   bool   `json:"device_flow_ready"`
	WebOAuthReady     bool   `json:"web_oauth_ready"`
	Hint              string `json:"hint,omitempty"`
}

// ListConfigs returns all configured servers (sorted by id).
func (m *Manager) ListConfigs() []ServerConfig {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerConfig, 0, len(m.cfgs))
	for _, c := range m.cfgs {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Status gives the live condition of each configured server. The code fills
// ToolCount when the server is connected, with ListTools, and that operation
// gives the best result that it can.
func (m *Manager) Status(ctx context.Context) []ServerStatus {
	cfgs := m.ListConfigs()
	out := make([]ServerStatus, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, m.statusOne(ctx, c))
	}
	return out
}

// StatusOf returns status for one configured server.
func (m *Manager) StatusOf(ctx context.Context, serverID string) (ServerStatus, bool) {
	c, ok := m.Config(serverID)
	if !ok {
		return ServerStatus{}, false
	}
	return m.statusOne(ctx, c), true
}

func (m *Manager) statusOne(ctx context.Context, c ServerConfig) ServerStatus {
	st := ServerStatus{
		ID:        c.ID,
		Name:      c.Name,
		Transport: c.Transport,
		URL:       c.URL,
		Command:   c.Command,
		Enabled:   c.Enabled,
		TokenEnv:  c.Auth.TokenEnv,
		IsGitHub:  isGitHub(c),
		Toolsets:  append([]string(nil), c.Toolsets...),
	}
	if st.TokenEnv == "" && isGitHub(c) {
		st.TokenEnv = ResolveGitHubTokenEnv()
	}
	if st.TokenEnv != "" {
		if isGitHubTokenEnv(st.TokenEnv) || isGitHub(c) {
			st.TokenConfigured = GitHubTokenPresent()
		} else {
			st.TokenConfigured = strings.TrimSpace(os.Getenv(st.TokenEnv)) != ""
		}
	} else if isGitHub(c) {
		st.TokenConfigured = GitHubTokenPresent()
		st.TokenEnv = ResolveGitHubTokenEnv()
	}
	err := m.Health(ctx, c.ID)
	st.Connected = err == nil
	st.HealthOK = err == nil
	if err != nil {
		st.HealthError = err.Error()
	}
	if st.Connected {
		tools, lerr := m.ListTools(ctx, c.ID)
		if lerr == nil {
			st.ToolCount = len(tools)
		}
	}
	return st
}

// Reconnect disconnects (if needed) then Connects using the stored config.
func (m *Manager) Reconnect(ctx context.Context, serverID string) error {
	cfg, ok := m.Config(serverID)
	if !ok {
		return ErrServerNotConfigured(serverID)
	}
	_ = m.Disconnect(ctx, serverID)
	_, err := m.Connect(ctx, cfg)
	return err
}

// ConnectConfigured connects a previously Configure'd server by id.
func (m *Manager) ConnectConfigured(ctx context.Context, serverID string) error {
	cfg, ok := m.Config(serverID)
	if !ok {
		return ErrServerNotConfigured(serverID)
	}
	_, err := m.Connect(ctx, cfg)
	return err
}

// The code returns ErrServerNotConfigured when it does not know id.
func ErrServerNotConfigured(id string) error {
	return &NotConfiguredError{ID: id}
}

// NotConfiguredError means the server id was never Configure'd.
type NotConfiguredError struct{ ID string }

func (e *NotConfiguredError) Error() string {
	return "mcp: server " + e.ID + " not configured"
}

// GitHubTokenPresent reports whether any GitHub token env alias or saved
// credential file is available.
func GitHubTokenPresent() bool {
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if strings.TrimSpace(lookupEnv(k)) != "" {
			return true
		}
	}
	if t, err := LoadGitHubTokenFile(); err == nil && strings.TrimSpace(t) != "" {
		return true
	}
	return false
}

// GitHubStatusSnapshot builds the GitHub MCP status card payload.
func (m *Manager) GitHubStatusSnapshot(ctx context.Context) GitHubStatus {
	env := ResolveGitHubTokenEnv()
	out := GitHubStatus{
		TokenConfigured:  GitHubTokenPresent(),
		TokenEnv:         env,
		TokenSource:      GitHubTokenSource(),
		RemoteURL:        GitHubRemoteURL,
		DockerImage:      GitHubDockerImage,
		OAuthClientIDSet: ResolveGitHubOAuthClientID() != "",
		OAuthSecretSet:   ResolveGitHubOAuthClientSecret() != "",
		DeviceFlowReady:  ResolveGitHubOAuthClientID() != "",
		WebOAuthReady:    ResolveGitHubOAuthClientID() != "" && ResolveGitHubOAuthClientSecret() != "",
	}
	if !out.TokenConfigured {
		if out.WebOAuthReady {
			out.Hint = "OAuth App ready — click Sign in with GitHub (browser). Callback must be http://127.0.0.1:8081/oauth/callback"
		} else if out.OAuthClientIDSet && !out.OAuthSecretSet {
			out.Hint = "Client ID loaded but CLIENT_SECRET missing in .env.local — required for classic OAuth App Sign in."
		} else {
			out.Hint = "Glider dashboard can be up while GitHub MCP is still disconnected. Sign in with GitHub or paste a PAT, then Connect."
		}
	} else if m == nil {
		out.Hint = "Token present but MCP manager not wired."
	}
	if m == nil {
		return out
	}
	if err := m.Health(ctx, "github"); err == nil {
		out.HTTPConnected = true
	} else if err != nil {
		out.HTTPHealthError = err.Error()
	}
	if err := m.Health(ctx, "github-stdio"); err == nil {
		out.StdioConnected = true
	} else if err != nil {
		out.StdioHealthError = err.Error()
	}
	if out.TokenConfigured && !out.HTTPConnected && !out.StdioConnected {
		out.Hint = "Token is configured but no MCP session is connected. Click Connect on github (HTTP) or github-stdio."
	}
	if out.HTTPConnected {
		out.Hint = "GitHub MCP HTTP session is live — tools list should show source=live."
	}
	return out
}

// ToolsListResult is the dashboard payload for /tools.
type ToolsListResult struct {
	Tools       []Tool `json:"tools"`
	Source      string `json:"source"` // live | catalog
	HealthError string `json:"health_error,omitempty"`
	Message     string `json:"message,omitempty"`
}

// ListToolsOrCatalogResult returns live tools when connected; for GitHub servers
// falls back to the documented catalog when disconnected, with an explicit message.
func (m *Manager) ListToolsOrCatalogResult(ctx context.Context, serverID string) (ToolsListResult, error) {
	cfg, ok := m.Config(serverID)
	if !ok {
		return ToolsListResult{}, ErrServerNotConfigured(serverID)
	}
	tools, err := m.ListTools(ctx, serverID)
	if err == nil {
		return ToolsListResult{
			Tools:   tools,
			Source:  "live",
			Message: "Live tools from connected MCP session.",
		}, nil
	}
	if isGitHub(cfg) {
		msg := "Documented catalog only — Glider is running, but this MCP server session is not connected."
		if err != nil {
			msg += " Reason: " + err.Error() + "."
		}
		if !GitHubTokenPresent() {
			msg += " Sign in with GitHub or paste a PAT on the MCP tab, then Connect."
		} else {
			msg += " Click Connect / Reconnect for this server to open a live session."
		}
		return ToolsListResult{
			Tools:       GitHubToolCatalog(cfg.Toolsets),
			Source:      "catalog",
			HealthError: err.Error(),
			Message:     msg,
		}, nil
	}
	return ToolsListResult{}, err
}

// ListToolsOrCatalog returns live tools when connected; for GitHub servers falls
// back to the documented catalog when disconnected.
func (m *Manager) ListToolsOrCatalog(ctx context.Context, serverID string) ([]Tool, string, error) {
	res, err := m.ListToolsOrCatalogResult(ctx, serverID)
	if err != nil {
		return nil, "", err
	}
	return res.Tools, res.Source, nil
}
