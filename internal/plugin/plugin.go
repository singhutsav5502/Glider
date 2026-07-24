// Package plugin defines the Glider plugin lifecycle and capability surface.
// Concrete plugins implement Plugin; the Host owns Register/Init/Start/Stop.
package plugin

import (
	"context"
	"encoding/json"
)

// State is plugin lifecycle state.
type State string

const (
	StateRegistered  State = "registered"
	StateInitialized State = "initialized"
	StateStarted     State = "started"
	StateStopped     State = "stopped"
	StateFailed      State = "failed"
)

// Capability flags what a plugin can provide.
type Capability string

const (
	CapTools     Capability = "tools"
	CapResources Capability = "resources"
	CapHooks     Capability = "hooks" // enter/exit stage hooks
	CapAuth      Capability = "auth"
)

// Meta is static plugin identity.
type Meta struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
}

// Health is a liveness/readiness report.
type Health struct {
	OK      bool   `json:"ok"`
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
}

// ToolSchema is a JSON-Schema-ish tool definition (OpenAI/MCP compatible shape).
type ToolSchema struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InputSchema is raw JSON Schema for arguments (object type).
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// OutputSchema optional.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ToolCall is one invocation request.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// RawInput is a plain-string fallback when Arguments is empty (hoop YAML).
	RawInput string `json:"raw_input,omitempty"`
}

// ToolResult is one invocation response.
type ToolResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Content string `json:"content,omitempty"`
	Err     string `json:"err,omitempty"`
	// IsError mirrors MCP isError semantics.
	IsError bool `json:"is_error,omitempty"`
}

// ToolProvider exposes list/call for tools owned by a plugin.
type ToolProvider interface {
	ListTools(ctx context.Context) ([]ToolSchema, error)
	CallTool(ctx context.Context, call ToolCall) (ToolResult, error)
}

// StageHook is the payload for CapHooks enter/exit callbacks.
type StageHook struct {
	Phase     string // enter | exit
	HoopID    string
	StageID   string
	StageKind string
	Iteration int
	Attrs     map[string]string
}

// HookProvider is optional CapHooks surface (enter/exit stage).
type HookProvider interface {
	OnStageEnter(ctx context.Context, h StageHook) error
	OnStageExit(ctx context.Context, h StageHook) error
}

// Plugin is the full lifecycle contract.
type Plugin interface {
	Meta() Meta
	Capabilities() []Capability

	// Register is called when added to a Host (before Init). Idempotent preferred.
	Register(ctx context.Context, host Host) error
	Init(ctx context.Context, cfg map[string]string) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health(ctx context.Context) Health

	// Tools may be nil if CapTools is absent.
	Tools() ToolProvider
	// Hooks may be nil if CapHooks is absent.
	Hooks() HookProvider
}

// Host is what the runtime provides to plugins (logging, shared context hooks).
type Host interface {
	// Emit records a structured event (typically agentlog / contextgraph).
	Emit(kind, message string, attrs map[string]string)
	Workspace() string // scoped root for filesystem tools
}

// Registry manages plugin instances by ID.
type Registry interface {
	Register(p Plugin) error
	Get(id string) (Plugin, bool)
	List() []Meta
	InitAll(ctx context.Context, cfg map[string]map[string]string) error
	StartAll(ctx context.Context) error
	StopAll(ctx context.Context) error
	HealthAll(ctx context.Context) map[string]Health
	// ToolProviders returns all started plugins that expose tools.
	ToolProviders() []ToolProvider
	// HookProviders returns plugins that expose CapHooks enter/exit.
	HookProviders() []HookProvider
}

// SimpleHost is a minimal Host for tests and default wiring.
type SimpleHost struct {
	Root   string
	OnEmit func(kind, message string, attrs map[string]string)
}

func (h *SimpleHost) Emit(kind, message string, attrs map[string]string) {
	if h != nil && h.OnEmit != nil {
		h.OnEmit(kind, message, attrs)
	}
}

func (h *SimpleHost) Workspace() string {
	if h == nil {
		return ""
	}
	return h.Root
}
