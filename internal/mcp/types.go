// Package mcp defines Glider's MCP client/server adapter interfaces and config.
// Concrete transport (stdio / HTTP) implementations live alongside; remote
// GitHub MCP is configured via ServerConfig (see github.go).
package mcp

import (
	"context"
	"encoding/json"
)

// TransportKind selects how Glider connects to an MCP server.
type TransportKind string

const (
	TransportStdio TransportKind = "stdio"
	TransportHTTP  TransportKind = "http"
	TransportSSE   TransportKind = "sse" // legacy SSE transport
)

// AuthKind for MCP servers.
type AuthKind string

const (
	AuthNone  AuthKind = "none"
	AuthBearer AuthKind = "bearer" // PAT / OAuth access token
	AuthHeader AuthKind = "header" // arbitrary header injection
	AuthEnv    AuthKind = "env"    // token from env var name
)

// AuthConfig holds credentials without embedding secrets in YAML when using AuthEnv.
type AuthConfig struct {
	Kind       AuthKind          `json:"kind,omitempty" yaml:"kind,omitempty"`
	Token      string            `json:"token,omitempty" yaml:"token,omitempty"`             // prefer env in production
	TokenEnv   string            `json:"token_env,omitempty" yaml:"token_env,omitempty"`   // e.g. GITHUB_PERSONAL_ACCESS_TOKEN
	HeaderName string            `json:"header_name,omitempty" yaml:"header_name,omitempty"` // default Authorization
	Extra      map[string]string `json:"extra,omitempty" yaml:"extra,omitempty"`
}

// ServerConfig is durable MCP server wiring (config / hoop node binding).
type ServerConfig struct {
	ID        string            `json:"id" yaml:"id"`
	Name      string            `json:"name,omitempty" yaml:"name,omitempty"`
	Transport TransportKind     `json:"transport,omitempty" yaml:"transport,omitempty"`
	// Command + Args for stdio (e.g. docker run ... ghcr.io/github/github-mcp-server).
	Command string   `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
	Env     []string `json:"env,omitempty" yaml:"env,omitempty"` // KEY=VALUE
	// URL for HTTP/SSE remote MCP (e.g. https://api.githubcopilot.com/mcp/).
	URL  string     `json:"url,omitempty" yaml:"url,omitempty"`
	Auth AuthConfig `json:"auth,omitempty" yaml:"auth,omitempty"`
	// Toolsets optionally filters server-side tool groups (GitHub: repos, issues, ...).
	Toolsets []string `json:"toolsets,omitempty" yaml:"toolsets,omitempty"`
	Enabled  bool     `json:"enabled" yaml:"enabled"`
}

// Tool is an MCP tool descriptor.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Resource is an MCP resource descriptor.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Prompt is an MCP prompt template descriptor.
type Prompt struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CallResult is tools/call response content.
type CallResult struct {
	Content string `json:"content,omitempty"`
	IsError bool   `json:"isError,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

// Notification is a server→client notification (progress, logging, etc.).
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Session is one connected MCP server session.
type Session interface {
	ID() string
	ServerID() string
	Close(ctx context.Context) error
}

// Client connects to MCP servers and invokes tools/resources/prompts.
type Client interface {
	Connect(ctx context.Context, cfg ServerConfig) (Session, error)
	Disconnect(ctx context.Context, serverID string) error
	ListTools(ctx context.Context, serverID string) ([]Tool, error)
	ListResources(ctx context.Context, serverID string) ([]Resource, error)
	ListPrompts(ctx context.Context, serverID string) ([]Prompt, error)
	CallTool(ctx context.Context, serverID, name string, args json.RawMessage) (CallResult, error)
	// OnNotification registers a handler for server notifications (optional).
	OnNotification(fn func(serverID string, n Notification))
	// Sessions returns active server IDs.
	Sessions() []string
	Health(ctx context.Context, serverID string) error
}

// ToolHandler is registered on a Glider-hosted MCP server adapter.
type ToolHandler func(ctx context.Context, name string, args json.RawMessage) (CallResult, error)

// ServerAdapter exposes Glider-native tools over MCP (so external hosts can call Glider).
type ServerAdapter interface {
	RegisterTool(tool Tool, handler ToolHandler) error
	UnregisterTool(name string) error
	ListRegistered() []Tool
	// Serve starts listening (stdio or HTTP). Blocks until ctx cancel unless RunAsync.
	Serve(ctx context.Context) error
	Stop(ctx context.Context) error
}

// NodeBinding declares which MCP servers/tools a graph node may use.
type NodeBinding struct {
	ServerID string   `json:"server_id" yaml:"server_id"`
	Tools    []string `json:"tools,omitempty" yaml:"tools,omitempty"` // empty = all from server
	Required bool     `json:"required,omitempty" yaml:"required,omitempty"`
}
