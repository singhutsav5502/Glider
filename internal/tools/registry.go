// Package tools is Glider's unified tool registry: native builtins + plugins + MCP.
// Hoop nodes and swarm workers bind tools via Ref; the Registry dispatches calls.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/glider-ai/glider/internal/mcp"
	"github.com/glider-ai/glider/internal/plugin"
)

// Kind classifies a tool source.
type Kind string

const (
	KindBuiltin Kind = "builtin"
	KindPlugin  Kind = "plugin"
	KindMCP     Kind = "mcp"
)

// Ref is a node-declared tool binding (YAML/JSON on StageSpec.tools).
type Ref struct {
	Name   string `json:"name" yaml:"name"`
	Kind   Kind   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Server string `json:"server,omitempty" yaml:"server,omitempty"` // MCP server id
	Plugin string `json:"plugin,omitempty" yaml:"plugin,omitempty"`
}

// Schema describes a tool for catalogs / LLM tool_choice.
type Schema struct {
	Name        string          `json:"name"`
	Kind        Kind            `json:"kind"`
	Description string          `json:"description,omitempty"`
	Server      string          `json:"server,omitempty"`
	Plugin      string          `json:"plugin,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// Result is one tool invocation outcome.
type Result struct {
	Name    string `json:"name"`
	Kind    Kind   `json:"kind,omitempty"`
	OK      bool   `json:"ok"`
	Output  string `json:"output,omitempty"`
	Err     string `json:"err,omitempty"`
	Stubbed bool   `json:"stubbed,omitempty"`
}

// Builtin is a native tool implementation.
type Builtin interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Call(ctx context.Context, input string, args json.RawMessage) (Result, error)
}

// ContextStore is the shared context query surface (implemented by contextgraph helpers).
type ContextStore interface {
	Query(turnID, q string, limit int) string
}

// Options configures the unified registry.
type Options struct {
	Workspace   string // sandbox root for fs/shell/git
	AllowHosts  []string
	AllowShell  bool
	ShellAllow  []string // command name allowlist when AllowShell
	Context     ContextStore
	MCP         mcp.Client
	Plugins     plugin.Registry
}

// Registry unifies native + MCP + plugin tools.
type Registry struct {
	mu       sync.RWMutex
	opts     Options
	builtins map[string]Builtin
}

// NewRegistry builds a registry and registers the standard builtin set.
func NewRegistry(opts Options) *Registry {
	r := &Registry{
		opts:     opts,
		builtins: make(map[string]Builtin),
	}
	for _, b := range StandardBuiltins(opts) {
		r.builtins[b.Name()] = b
	}
	return r
}

// RegisterBuiltin adds or replaces a native tool.
func (r *Registry) RegisterBuiltin(b Builtin) {
	if r == nil || b == nil {
		return
	}
	r.mu.Lock()
	r.builtins[b.Name()] = b
	r.mu.Unlock()
}

// SetMCP swaps the MCP client.
func (r *Registry) SetMCP(c mcp.Client) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.opts.MCP = c
	r.mu.Unlock()
}

// SetPlugins swaps the plugin registry.
func (r *Registry) SetPlugins(p plugin.Registry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.opts.Plugins = p
	r.mu.Unlock()
}

// Catalog returns all known tool schemas (builtins + plugin + connected MCP).
func (r *Registry) Catalog(ctx context.Context) []Schema {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Schema
	for _, b := range r.builtins {
		out = append(out, Schema{
			Name: b.Name(), Kind: KindBuiltin, Description: b.Description(), InputSchema: b.InputSchema(),
		})
	}
	if r.opts.Plugins != nil {
		for _, tp := range r.opts.Plugins.ToolProviders() {
			tools, err := tp.ListTools(ctx)
			if err != nil {
				continue
			}
			for _, t := range tools {
				out = append(out, Schema{
					Name: t.Name, Kind: KindPlugin, Description: t.Description, InputSchema: t.InputSchema,
				})
			}
		}
	}
	if r.opts.MCP != nil {
		for _, sid := range r.opts.MCP.Sessions() {
			tools, err := r.opts.MCP.ListTools(ctx, sid)
			if err != nil {
				continue
			}
			for _, t := range tools {
				out = append(out, Schema{
					Name: t.Name, Kind: KindMCP, Server: sid, Description: t.Description, InputSchema: t.InputSchema,
				})
			}
		}
	}
	return out
}

// Invoke dispatches a Ref.
func (r *Registry) Invoke(ctx context.Context, ref Ref, input string) (Result, error) {
	if r == nil {
		return Result{Name: ref.Name, OK: false, Err: "nil registry"}, fmt.Errorf("nil registry")
	}
	kind := ref.Kind
	if kind == "" {
		kind = KindBuiltin
	}
	switch kind {
	case KindMCP:
		return r.invokeMCP(ctx, ref, input)
	case KindPlugin:
		return r.invokePlugin(ctx, ref, input)
	default:
		return r.invokeBuiltin(ctx, ref, input)
	}
}

// InvokeAll runs refs sequentially.
func (r *Registry) InvokeAll(ctx context.Context, refs []Ref, input string) []Result {
	var out []Result
	for _, ref := range refs {
		res, err := r.Invoke(ctx, ref, input)
		if err != nil && res.Err == "" {
			res.Err = err.Error()
			res.OK = false
		}
		out = append(out, res)
	}
	return out
}

func (r *Registry) invokeBuiltin(ctx context.Context, ref Ref, input string) (Result, error) {
	r.mu.RLock()
	b := r.builtins[ref.Name]
	r.mu.RUnlock()
	if b == nil {
		return Result{Name: ref.Name, Kind: KindBuiltin, OK: false, Err: "unknown builtin"}, fmt.Errorf("unknown builtin %q", ref.Name)
	}
	res, err := b.Call(ctx, input, nil)
	res.Name = ref.Name
	res.Kind = KindBuiltin
	return res, err
}

func (r *Registry) invokePlugin(ctx context.Context, ref Ref, input string) (Result, error) {
	r.mu.RLock()
	preg := r.opts.Plugins
	r.mu.RUnlock()
	if preg == nil {
		return Result{Name: ref.Name, Kind: KindPlugin, OK: true, Stubbed: true,
			Output: "plugin registry empty"}, nil
	}
	id := ref.Plugin
	if id == "" {
		id = ref.Name
	}
	p, ok := preg.Get(id)
	if !ok || p.Tools() == nil {
		return Result{Name: ref.Name, Kind: KindPlugin, OK: true, Stubbed: true,
			Output: fmt.Sprintf("plugin %q not found", id)}, nil
	}
	tr, err := p.Tools().CallTool(ctx, plugin.ToolCall{Name: ref.Name, RawInput: input})
	if err != nil {
		return Result{Name: ref.Name, Kind: KindPlugin, OK: false, Err: err.Error()}, err
	}
	return Result{Name: ref.Name, Kind: KindPlugin, OK: tr.OK && !tr.IsError, Output: tr.Content, Err: tr.Err}, nil
}

func (r *Registry) invokeMCP(ctx context.Context, ref Ref, input string) (Result, error) {
	r.mu.RLock()
	client := r.opts.MCP
	r.mu.RUnlock()
	if client == nil {
		return Result{Name: ref.Name, Kind: KindMCP, OK: true, Stubbed: true,
			Output: fmt.Sprintf("mcp stub server=%s tool=%s", ref.Server, ref.Name)}, nil
	}
	args := json.RawMessage(`{}`)
	if strings.TrimSpace(input) != "" {
		b, _ := json.Marshal(map[string]string{"input": input})
		args = b
	}
	cr, err := client.CallTool(ctx, ref.Server, ref.Name, args)
	if err != nil {
		return Result{Name: ref.Name, Kind: KindMCP, OK: false, Err: err.Error()}, err
	}
	return Result{Name: ref.Name, Kind: KindMCP, OK: !cr.IsError, Output: cr.Content, Stubbed: strings.Contains(cr.Content, "stub")}, nil
}

// StandardNames lists the core builtin tool names.
func StandardNames() []string {
	return []string{
		"fs_read", "fs_write", "fs_list", "fs_search",
		"code_grep",
		"shell_exec",
		"http_fetch",
		"git_status", "git_diff", "git_log", "git_clone",
		"context_query",
		"datetime", "calculator",
	}
}
