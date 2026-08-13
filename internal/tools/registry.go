// Package tools is Glider's unified tool registry: native builtins + plugins + MCP.
// Hoop nodes and swarm workers bind tools via Ref; the Registry dispatches calls.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	Workspace  string // sandbox root for fs/shell/git
	AllowHosts []string
	AllowShell bool
	ShellAllow []string // command name allowlist when AllowShell
	WebSearch  WebSearchOptions
	Context    ContextStore
	MCP        mcp.Client
	Plugins    plugin.Registry
}

// DefaultWorkspaceDir returns ~/.glider/workspace (process-local agent sandbox).
func DefaultWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "workspace")
	}
	return filepath.Join(home, ".glider", "workspace")
}

// ResolveWorkspace picks cfg path, expands ~, or falls back to DefaultWorkspaceDir.
// Creates the directory when missing.
func ResolveWorkspace(configured string) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	if strings.HasPrefix(root, "~/") || root == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if root == "~" {
			root = home
		} else {
			root = filepath.Join(home, root[2:])
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

// Registry unifies native + MCP + plugin tools.
type Registry struct {
	mu        sync.RWMutex
	opts      Options
	builtins  map[string]Builtin
	runID     string // active hoop / swarm run for artifact_write layout
	layout    RunLayout
	hasLayout bool
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
	// Run-scoped tools need the registry for active run id (same RelWork mapping as git_clone).
	r.builtins["artifact_write"] = &artifactWrite{reg: r, root: opts.Workspace}
	r.builtins["git_clone"] = &gitClone{reg: r, root: opts.Workspace}
	r.builtins["fs_read"] = &fsRead{reg: r, root: opts.Workspace}
	r.builtins["fs_write"] = &fsWrite{reg: r, root: opts.Workspace}
	r.builtins["fs_list"] = &fsList{reg: r, root: opts.Workspace}
	r.builtins["fs_search"] = &fsSearch{reg: r, root: opts.Workspace}
	r.builtins["code_grep"] = &codeGrep{reg: r, root: opts.Workspace}
	return r
}

// SetRunID sets the active run folder (hoop id or swarm turn id) for artifact tools.
func (r *Registry) SetRunID(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.runID = SanitizeRunID(id)
	r.mu.Unlock()
}

// RunID returns the active sanitized run id (may be empty).
func (r *Registry) RunID() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.runID
}

// Workspace returns the configured sandbox root.
func (r *Registry) Workspace() string {
	if r == nil {
		return ""
	}
	return r.opts.Workspace
}

// EnsureRunLayout creates runs/<id>/{work,out} and records the association.
func (r *Registry) EnsureRunLayout(runID string) (RunLayout, error) {
	if r == nil {
		return RunLayout{}, fmt.Errorf("nil registry")
	}
	lay := LayoutForRun(r.opts.Workspace, runID)
	if err := lay.Ensure(); err != nil {
		return lay, err
	}
	r.mu.Lock()
	r.runID = lay.RunID
	r.layout = lay
	r.hasLayout = true
	r.mu.Unlock()
	return lay, nil
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
	if layout.RootAbs != "" {
		r.opts.Workspace = layout.RootAbs
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
	layout, err := LayoutExisting(r.Workspace(), runID, workPath, outPath)
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

// ActiveLayout prefers ctx-bound layout, then registry fallback.
func (r *Registry) ActiveLayout(ctx context.Context) (RunLayout, bool) {
	if l, ok := RunLayoutFrom(ctx); ok {
		return l, true
	}
	return r.CurrentLayout()
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
	var args json.RawMessage
	trimmed := strings.TrimSpace(input)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid([]byte(trimmed)) {
		args = json.RawMessage(trimmed)
	}
	res, err := b.Call(ctx, input, args)
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
	// Wildcard / dashboard list_tools → return live catalog (not CallTool).
	if ref.Name == "*" || ref.Name == "list_tools" {
		return r.listMCPCatalog(ctx, ref.Server)
	}
	args := json.RawMessage(`{}`)
	trimmed := strings.TrimSpace(input)
	if trimmed != "" {
		if trimmed[0] == '{' && json.Valid([]byte(trimmed)) {
			args = json.RawMessage(trimmed)
		} else {
			b, _ := json.Marshal(map[string]string{"input": input})
			args = b
		}
	}
	cr, err := client.CallTool(ctx, ref.Server, ref.Name, args)
	if err != nil {
		return Result{Name: ref.Name, Kind: KindMCP, OK: false, Err: err.Error()}, err
	}
	stubbed := strings.Contains(strings.ToLower(cr.Content), "mcp stub") ||
		strings.Contains(strings.ToLower(cr.Content), "github mcp stub")
	return Result{Name: ref.Name, Kind: KindMCP, OK: !cr.IsError, Output: cr.Content, Stubbed: stubbed}, nil
}

func (r *Registry) listMCPCatalog(ctx context.Context, serverID string) (Result, error) {
	r.mu.RLock()
	client := r.opts.MCP
	r.mu.RUnlock()
	if client == nil {
		return Result{Name: "list_tools", Kind: KindMCP, OK: true, Stubbed: true,
			Output: "mcp client not configured"}, nil
	}
	tools, err := client.ListTools(ctx, serverID)
	if err != nil {
		return Result{Name: "list_tools", Kind: KindMCP, OK: false, Err: err.Error()}, err
	}
	var b strings.Builder
	b.WriteString("mcp tools on " + serverID + ":\n")
	for _, t := range tools {
		b.WriteString("- ")
		b.WriteString(t.Name)
		if t.Description != "" {
			b.WriteString(": ")
			b.WriteString(t.Description)
		}
		b.WriteByte('\n')
	}
	return Result{Name: "list_tools", Kind: KindMCP, OK: true, Output: b.String()}, nil
}

// ExpandRefs replaces MCP "*" / "list_tools" wildcards with live ListTools entries.
// Never leaves Name="*" in the result — CallTool("*") is invalid on MCP servers.
func (r *Registry) ExpandRefs(ctx context.Context, refs []Ref) []Ref {
	if r == nil || len(refs) == 0 {
		return refs
	}
	r.mu.RLock()
	client := r.opts.MCP
	r.mu.RUnlock()
	var out []Ref
	seen := map[string]bool{}
	add := func(ref Ref) {
		key := ref.Name + "|" + string(ref.Kind) + "|" + ref.Server
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ref)
	}
	for _, ref := range refs {
		kind := ref.Kind
		if kind == "" {
			kind = KindBuiltin
		}
		name := strings.TrimSpace(ref.Name)
		isWild := name == "*" || name == "list_tools"
		// Wildcard with a server is always MCP, even if kind was omitted / wrong.
		if isWild && ref.Server != "" {
			kind = KindMCP
		}
		if kind == KindMCP && isWild {
			if ref.Server == "" {
				// Drop bare wildcards — never CallTool("*").
				continue
			}
			if client != nil {
				tools, err := client.ListTools(ctx, ref.Server)
				if err == nil && len(tools) > 0 {
					for _, t := range tools {
						add(Ref{Name: t.Name, Kind: KindMCP, Server: ref.Server})
					}
					continue
				}
			}
			// Catalog probe so blind/agent paths can still list tools (not CallTool("*")).
			add(Ref{Name: "list_tools", Kind: KindMCP, Server: ref.Server})
			continue
		}
		ref.Kind = kind
		add(ref)
	}
	return out
}

// BlindSafe reports whether a tool can be invoked with BlindPrepassInput (workspace ".") .
// Tools that need structured args (clone URL, write path, search query, MCP calls)
// must only run via the agent loop — never with the hoop goal string.
func BlindSafe(ref Ref) bool {
	name := strings.TrimSpace(ref.Name)
	kind := ref.Kind
	if kind == "" {
		kind = KindBuiltin
	}
	if name == "*" {
		return false
	}
	if kind == KindMCP {
		// "*" must be ExpandRefs'd first; only the catalog probe is blind-safe.
		return name == "list_tools"
	}
	switch name {
	case "datetime", "fs_list", "git_status", "git_diff", "git_log":
		return true
	default:
		// context_query / code_grep / fs_search / writes / clone need real args.
		return false
	}
}

// BlindPrepassInput is the only input used for FilterBlindSafe pre-invokes.
// Never pass the hoop goal or stage prompt — that poisoned fs_write/artifact_write
// and made context_query/code_grep search prose.
func BlindPrepassInput() string { return "." }

// FilterBlindSafe keeps only tools safe for InvokeAllParallel(BlindPrepassInput()) .
func FilterBlindSafe(refs []Ref) []Ref {
	var out []Ref
	for _, ref := range refs {
		if BlindSafe(ref) {
			out = append(out, ref)
		}
	}
	return out
}

// StandardNames lists the core builtin tool names.
func StandardNames() []string {
	return []string{
		"fs_read", "fs_write", "fs_list", "fs_search",
		"artifact_write",
		"code_grep",
		"shell_exec",
		"http_fetch",
		"web_search", "web_fetch",
		"git_status", "git_diff", "git_log", "git_clone",
		"context_query",
		"datetime", "calculator",
	}
}

// OpenAIToolsJSON builds an OpenAI-compatible tools[] array from schemas / refs.
func (r *Registry) OpenAIToolsJSON(ctx context.Context, refs []Ref) json.RawMessage {
	if r == nil {
		return json.RawMessage("[]")
	}
	refs = r.ExpandRefs(ctx, refs)
	var schemas []Schema
	if len(refs) == 0 {
		schemas = r.Catalog(ctx)
	} else {
		cat := r.Catalog(ctx)
		byKey := map[string]Schema{}
		for _, s := range cat {
			byKey[s.Name+"|"+string(s.Kind)+"|"+s.Server] = s
			byKey[s.Name] = s
		}
		for _, ref := range refs {
			if s, ok := byKey[ref.Name+"|"+string(ref.Kind)+"|"+ref.Server]; ok {
				schemas = append(schemas, s)
				continue
			}
			if s, ok := byKey[ref.Name]; ok {
				schemas = append(schemas, s)
				continue
			}
			// Minimal schema for declared but unknown tools.
			schemas = append(schemas, Schema{
				Name: ref.Name, Kind: ref.Kind, Server: ref.Server,
				Description: "declared tool " + ref.Name,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
			})
		}
	}
	type fn struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	type tool struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	out := make([]tool, 0, len(schemas))
	for _, s := range schemas {
		if strings.TrimSpace(s.Name) == "*" {
			// Never advertise a literal "*" tool — models would CallTool("*").
			continue
		}
		params := s.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, tool{
			Type: "function",
			Function: fn{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  params,
			},
		})
	}
	b, _ := json.Marshal(out)
	return b
}

// InvokeAllParallel runs refs concurrently (bounded by GOMAXPROCS-ish wait group).
// Always ExpandRefs first so MCP "*" never reaches CallTool.
func (r *Registry) InvokeAllParallel(ctx context.Context, refs []Ref, input string) []Result {
	if r == nil || len(refs) == 0 {
		return nil
	}
	refs = r.ExpandRefs(ctx, refs)
	if len(refs) == 0 {
		return nil
	}
	out := make([]Result, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref Ref) {
			defer wg.Done()
			res, err := r.Invoke(ctx, ref, input)
			if err != nil && res.Err == "" {
				res.Err = err.Error()
				res.OK = false
			}
			out[i] = res
		}(i, ref)
	}
	wg.Wait()
	return out
}

// InvokeAll runs refs sequentially (kept for compatibility).
func (r *Registry) InvokeAll(ctx context.Context, refs []Ref, input string) []Result {
	if r == nil || len(refs) == 0 {
		return nil
	}
	refs = r.ExpandRefs(ctx, refs)
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
