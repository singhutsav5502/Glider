// Package tools is Glider's unified tool registry: native builtins + plugins + MCP.
// Hoop nodes and swarm workers bind tools via Ref; the Registry dispatches calls.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// ContextStoreRaw optionally supports filter-syntax queries (prov=/path=/neigh=).
type ContextStoreRaw interface {
	QueryRaw(input string) string
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
	mu        sync.RWMutex
	opts      Options
	builtins  map[string]Builtin
	runID     string
	layout    RunLayout
	hasLayout bool
}

// NewRegistry builds a registry and registers the standard builtin set.
func NewRegistry(opts Options) *Registry {
	if strings.TrimSpace(opts.Workspace) == "" {
		opts.Workspace = DefaultWorkspaceDir()
	}
	if abs, err := filepath.Abs(opts.Workspace); err == nil {
		opts.Workspace = abs
	}
	r := &Registry{
		opts:     opts,
		builtins: make(map[string]Builtin),
	}
	for _, b := range StandardBuiltins(opts) {
		r.builtins[b.Name()] = b
	}
	return r
}

// Workspace returns the sandbox root (typically ~/.glider/workspace).
func (r *Registry) Workspace() string {
	if r == nil {
		return DefaultWorkspaceDir()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opts.Workspace
}

// SetRunID records the active run id (hoop id / swarm turn id) without creating dirs.
func (r *Registry) SetRunID(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.runID = sanitizeRunID(id)
	r.mu.Unlock()
}

// RunID returns the last SetRunID / EnsureRunLayout id.
func (r *Registry) RunID() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.runID
}

// EnsureRunLayout creates runs/<run_id>/{work,out} under the workspace and
// records the association on the registry (fallback when ctx has no layout).
func (r *Registry) EnsureRunLayout(runID string) (RunLayout, error) {
	if r == nil {
		return RunLayout{}, fmt.Errorf("nil registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	root := r.opts.Workspace
	if root == "" {
		root = DefaultWorkspaceDir()
		r.opts.Workspace = root
	}
	layout := LayoutForRun(root, runID)
	if err := layout.Ensure(); err != nil {
		return RunLayout{}, err
	}
	r.runID = layout.RunID
	r.layout = layout
	r.hasLayout = true
	return layout, nil
}

// BindLayout installs a pre-validated layout (e.g. workspace stage mode=existing).
func (r *Registry) BindLayout(layout RunLayout) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	if err := layout.Ensure(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if layout.WorkspaceRoot != "" {
		r.opts.Workspace = layout.WorkspaceRoot
	}
	if layout.RunID != "" {
		r.runID = layout.RunID
	}
	r.layout = layout
	r.hasLayout = true
	return nil
}

// BindExisting resolves workspace_path (+ optional out_path) under the sandbox
// and binds it as the active work/out roots for subsequent tool calls.
func (r *Registry) BindExisting(runID, workPath, outPath string) (RunLayout, error) {
	if r == nil {
		return RunLayout{}, fmt.Errorf("nil registry")
	}
	root := r.Workspace()
	layout, err := LayoutExisting(root, runID, workPath, outPath)
	if err != nil {
		return RunLayout{}, err
	}
	if err := r.BindLayout(layout); err != nil {
		return RunLayout{}, err
	}
	return layout, nil
}

// CurrentLayout returns the registry-bound layout, if any.
func (r *Registry) CurrentLayout() (RunLayout, bool) {
	if r == nil {
		return RunLayout{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.hasLayout {
		return RunLayout{}, false
	}
	return r.layout, true
}

// ClearRunLayout drops the fallback layout association.
func (r *Registry) ClearRunLayout() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.hasLayout = false
	r.layout = RunLayout{}
	r.runID = ""
	r.mu.Unlock()
}

// ActiveLayout prefers ctx-bound layout, then registry fallback.
func (r *Registry) ActiveLayout(ctx context.Context) (RunLayout, bool) {
	if l, ok := RunLayoutFrom(ctx); ok {
		return l, true
	}
	return r.CurrentLayout()
}

// ResolvePath scopes a tool path (bare → work; kind=out → out) and safeJoins under workspace.
func (r *Registry) ResolvePath(ctx context.Context, rel string, kind ArtifactKind) (string, error) {
	root := ""
	if r != nil {
		root = r.Workspace()
	}
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	if l, ok := r.ActiveLayout(ctx); ok {
		return l.ResolveAbs(rel, kind)
	}
	return safeJoin(root, strings.TrimSpace(rel))
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
	stubbed := strings.Contains(strings.ToLower(cr.Content), "mcp stub") ||
		strings.Contains(strings.ToLower(cr.Content), "github mcp stub")
	return Result{Name: ref.Name, Kind: KindMCP, OK: !cr.IsError, Output: cr.Content, Stubbed: stubbed}, nil
}

// StandardNames lists the core builtin tool names.
func StandardNames() []string {
	return []string{
		"fs_read", "fs_write", "fs_list", "fs_search",
		"code_grep",
		"artifact_write",
		"shell_exec",
		"http_fetch",
		"git_status", "git_diff", "git_log", "git_clone",
		"context_query",
		"datetime", "calculator",
	}
}
