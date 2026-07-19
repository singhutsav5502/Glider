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
	TokenConfigured bool   `json:"token_configured"`
	TokenEnv        string `json:"token_env,omitempty"` // which env name resolved (never the secret)
	HTTPConnected   bool   `json:"http_connected"`
	StdioConnected  bool   `json:"stdio_connected"`
	RemoteURL       string `json:"remote_url"`
	DockerImage     string `json:"docker_image"`
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

// Status returns live status for every configured server.
// ToolCount is filled when connected (best-effort ListTools).
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

// ErrServerNotConfigured is returned when id is unknown.
func ErrServerNotConfigured(id string) error {
	return &NotConfiguredError{ID: id}
}

// NotConfiguredError means the server id was never Configure'd.
type NotConfiguredError struct{ ID string }

func (e *NotConfiguredError) Error() string {
	return "mcp: server " + e.ID + " not configured"
}

// GitHubTokenPresent reports whether any GitHub token env alias is set (non-empty).
func GitHubTokenPresent() bool {
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if strings.TrimSpace(lookupEnv(k)) != "" {
			return true
		}
	}
	return false
}

// GitHubStatusSnapshot builds the GitHub MCP status card payload.
func (m *Manager) GitHubStatusSnapshot(ctx context.Context) GitHubStatus {
	env := ResolveGitHubTokenEnv()
	out := GitHubStatus{
		TokenConfigured: GitHubTokenPresent(),
		TokenEnv:        env,
		RemoteURL:       GitHubRemoteURL,
		DockerImage:     GitHubDockerImage,
	}
	if m == nil {
		return out
	}
	if err := m.Health(ctx, "github"); err == nil {
		out.HTTPConnected = true
	}
	if err := m.Health(ctx, "github-stdio"); err == nil {
		out.StdioConnected = true
	}
	return out
}

// ListToolsOrCatalog returns live tools when connected; for GitHub servers falls
// back to the documented catalog when disconnected.
func (m *Manager) ListToolsOrCatalog(ctx context.Context, serverID string) ([]Tool, string, error) {
	cfg, ok := m.Config(serverID)
	if !ok {
		return nil, "", ErrServerNotConfigured(serverID)
	}
	tools, err := m.ListTools(ctx, serverID)
	if err == nil {
		return tools, "live", nil
	}
	if isGitHub(cfg) {
		return GitHubToolCatalog(cfg.Toolsets), "catalog", nil
	}
	return nil, "", err
}
