package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ResolveAuth expands AuthEnv into Token from the process environment.
func ResolveAuth(a AuthConfig) (AuthConfig, error) {
	out := a
	if out.Kind == "" {
		if out.TokenEnv != "" || out.Token != "" {
			out.Kind = AuthBearer
		} else {
			out.Kind = AuthNone
		}
	}
	if out.Kind == AuthEnv || (out.Token == "" && out.TokenEnv != "") {
		env := out.TokenEnv
		if env == "" {
			return out, fmt.Errorf("auth: token_env required")
		}
		v := os.Getenv(env)
		if v == "" {
			return out, fmt.Errorf("auth: env %s empty", env)
		}
		out.Token = v
		if out.Kind == AuthEnv {
			out.Kind = AuthBearer
		}
	}
	if out.HeaderName == "" && out.Kind == AuthBearer {
		out.HeaderName = "Authorization"
	}
	return out, nil
}

// AuthorizationHeader returns "Bearer <token>" or empty.
func AuthorizationHeader(a AuthConfig) string {
	resolved, err := ResolveAuth(a)
	if err != nil || resolved.Token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(resolved.Token), "bearer ") {
		return resolved.Token
	}
	return "Bearer " + resolved.Token
}

// Manager is a multi-server Client with stub transport until stdio/HTTP land.
// CallTool returns clear stub payloads when a server is "connected" in-memory only.
type Manager struct {
	mu       sync.RWMutex
	cfgs     map[string]ServerConfig
	sessions map[string]bool
	notify   func(serverID string, n Notification)
	// Real is optional concrete client (stdio/http); when set, Connect delegates.
	Real Client
}

// NewManager constructs an empty MCP manager.
func NewManager() *Manager {
	return &Manager{
		cfgs:     make(map[string]ServerConfig),
		sessions: make(map[string]bool),
	}
}

// Configure stores server configs (does not connect).
func (m *Manager) Configure(cfgs ...ServerConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cfgs {
		c.ID = strings.TrimSpace(c.ID)
		if c.ID == "" {
			continue
		}
		m.cfgs[c.ID] = c
	}
}

func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) (Session, error) {
	if m == nil {
		return nil, fmt.Errorf("nil mcp manager")
	}
	if m.Real != nil {
		return m.Real.Connect(ctx, cfg)
	}
	cfg.ID = strings.TrimSpace(cfg.ID)
	if cfg.ID == "" {
		return nil, fmt.Errorf("server id required")
	}
	if _, err := ResolveAuth(cfg.Auth); err != nil && cfg.Auth.Kind != AuthNone && cfg.Auth.Kind != "" {
		// Allow connect without token for local stub demos; record message.
		_ = err
	}
	m.mu.Lock()
	m.cfgs[cfg.ID] = cfg
	m.sessions[cfg.ID] = true
	m.mu.Unlock()
	return stubSession{id: cfg.ID}, nil
}

func (m *Manager) Disconnect(ctx context.Context, serverID string) error {
	if m != nil && m.Real != nil {
		return m.Real.Disconnect(ctx, serverID)
	}
	m.mu.Lock()
	delete(m.sessions, serverID)
	m.mu.Unlock()
	return nil
}

func (m *Manager) ListTools(ctx context.Context, serverID string) ([]Tool, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListTools(ctx, serverID)
	}
	m.mu.RLock()
	cfg, ok := m.cfgs[serverID]
	alive := m.sessions[serverID]
	m.mu.RUnlock()
	if !ok || !alive {
		return nil, fmt.Errorf("mcp: server %q not connected", serverID)
	}
	// GitHub-shaped catalog when server id/name suggests github.
	if isGitHub(cfg) {
		return GitHubToolCatalog(cfg.Toolsets), nil
	}
	return []Tool{{
		Name:        "ping",
		Description: "Stub MCP ping for " + serverID,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}, nil
}

func (m *Manager) ListResources(ctx context.Context, serverID string) ([]Resource, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListResources(ctx, serverID)
	}
	return nil, nil
}

func (m *Manager) ListPrompts(ctx context.Context, serverID string) ([]Prompt, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListPrompts(ctx, serverID)
	}
	return nil, nil
}

func (m *Manager) CallTool(ctx context.Context, serverID, name string, args json.RawMessage) (CallResult, error) {
	if m != nil && m.Real != nil {
		return m.Real.CallTool(ctx, serverID, name, args)
	}
	m.mu.RLock()
	alive := m.sessions[serverID]
	cfg := m.cfgs[serverID]
	m.mu.RUnlock()
	if !alive {
		return CallResult{}, fmt.Errorf("mcp: server %q not connected", serverID)
	}
	msg := fmt.Sprintf("mcp stub call server=%s tool=%s transport=%s", serverID, name, cfg.Transport)
	if isGitHub(cfg) {
		msg = fmt.Sprintf("github mcp stub: tool=%s (wire GITHUB_PERSONAL_ACCESS_TOKEN + docker/http client for live calls)", name)
	}
	return CallResult{Content: msg, IsError: false}, nil
}

func (m *Manager) OnNotification(fn func(serverID string, n Notification)) {
	if m == nil {
		return
	}
	m.notify = fn
	if m.Real != nil {
		m.Real.OnNotification(fn)
	}
}

func (m *Manager) Sessions() []string {
	if m != nil && m.Real != nil {
		return m.Real.Sessions()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	return out
}

func (m *Manager) Health(ctx context.Context, serverID string) error {
	if m != nil && m.Real != nil {
		return m.Real.Health(ctx, serverID)
	}
	m.mu.RLock()
	ok := m.sessions[serverID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("not connected")
	}
	return nil
}

type stubSession struct{ id string }

func (s stubSession) ID() string                         { return s.id }
func (s stubSession) ServerID() string                   { return s.id }
func (s stubSession) Close(context.Context) error        { return nil }

func isGitHub(cfg ServerConfig) bool {
	id := strings.ToLower(cfg.ID + " " + cfg.Name + " " + cfg.URL + " " + cfg.Command)
	return strings.Contains(id, "github")
}

// Ensure Manager implements Client.
var _ Client = (*Manager)(nil)
