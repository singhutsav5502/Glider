package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// liveSession is the internal interface for stdio/http sessions.
type liveSession interface {
	Session
	listTools(ctx context.Context) ([]Tool, error)
	callTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error)
	listResources(ctx context.Context) ([]Resource, error)
	listPrompts(ctx context.Context) ([]Prompt, error)
}

// Manager is a multi-server MCP Client with live stdio and HTTP transports.
type Manager struct {
	mu       sync.RWMutex
	cfgs     map[string]ServerConfig
	sessions map[string]liveSession
	notify   func(serverID string, n Notification)
	// Real is optional override Client (tests / custom transports).
	Real Client
}

// NewManager constructs an empty MCP manager.
func NewManager() *Manager {
	return &Manager{
		cfgs:     make(map[string]ServerConfig),
		sessions: make(map[string]liveSession),
	}
}

// Configure stores server configs (does not connect). Validates each config.
func (m *Manager) Configure(cfgs ...ServerConfig) error {
	if m == nil {
		return fmt.Errorf("nil mcp manager")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cfgs {
		c := c
		if err := ValidateServerConfig(&c); err != nil {
			return err
		}
		m.cfgs[c.ID] = c
	}
	return nil
}

func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) (Session, error) {
	if m == nil {
		return nil, fmt.Errorf("nil mcp manager")
	}
	if m.Real != nil {
		return m.Real.Connect(ctx, cfg)
	}
	if err := ValidateServerConfig(&cfg); err != nil {
		return nil, err
	}
	notify := func(n Notification) {
		if m.notify != nil {
			m.notify(cfg.ID, n)
		}
	}

	var sess liveSession
	var err error
	tr := cfg.Transport
	if tr == "" {
		if cfg.URL != "" {
			tr = TransportHTTP
		} else {
			tr = TransportStdio
		}
	}
	switch tr {
	case TransportHTTP, TransportSSE:
		sess, err = startHTTPSession(ctx, cfg, notify)
	case TransportStdio:
		sess, err = startStdioSession(ctx, cfg, notify)
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q", tr)
	}
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	// Replace prior session for same id.
	if old, ok := m.sessions[cfg.ID]; ok {
		_ = old.Close(context.Background())
	}
	m.cfgs[cfg.ID] = cfg
	m.sessions[cfg.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

func (m *Manager) Disconnect(ctx context.Context, serverID string) error {
	if m != nil && m.Real != nil {
		return m.Real.Disconnect(ctx, serverID)
	}
	m.mu.Lock()
	sess := m.sessions[serverID]
	delete(m.sessions, serverID)
	m.mu.Unlock()
	if sess != nil {
		return sess.Close(ctx)
	}
	return nil
}

func (m *Manager) get(serverID string) (liveSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess := m.sessions[serverID]
	if sess == nil {
		return nil, fmt.Errorf("mcp: server %q not connected", serverID)
	}
	return sess, nil
}

func (m *Manager) ListTools(ctx context.Context, serverID string) ([]Tool, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListTools(ctx, serverID)
	}
	sess, err := m.get(serverID)
	if err != nil {
		return nil, err
	}
	return sess.listTools(ctx)
}

func (m *Manager) ListResources(ctx context.Context, serverID string) ([]Resource, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListResources(ctx, serverID)
	}
	sess, err := m.get(serverID)
	if err != nil {
		return nil, err
	}
	return sess.listResources(ctx)
}

func (m *Manager) ListPrompts(ctx context.Context, serverID string) ([]Prompt, error) {
	if m != nil && m.Real != nil {
		return m.Real.ListPrompts(ctx, serverID)
	}
	sess, err := m.get(serverID)
	if err != nil {
		return nil, err
	}
	return sess.listPrompts(ctx)
}

func (m *Manager) CallTool(ctx context.Context, serverID, name string, args json.RawMessage) (CallResult, error) {
	if m != nil && m.Real != nil {
		return m.Real.CallTool(ctx, serverID, name, args)
	}
	sess, err := m.get(serverID)
	if err != nil {
		return CallResult{}, err
	}
	return sess.callTool(ctx, name, args)
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
	_, err := m.get(serverID)
	return err
}

// Config returns a copy of a stored server config.
func (m *Manager) Config(serverID string) (ServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.cfgs[serverID]
	return c, ok
}

func isGitHub(cfg ServerConfig) bool {
	id := strings.ToLower(cfg.ID + " " + cfg.Name + " " + cfg.URL + " " + cfg.Command)
	return strings.Contains(id, "github")
}

// Ensure Manager implements Client.
var _ Client = (*Manager)(nil)
