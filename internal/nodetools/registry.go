// Package nodetools is a thin compatibility facade over internal/tools + mcp + plugin.
// Prefer github.com/glider-ai/glider/internal/tools for new code.
package nodetools

import (
	"context"

	"github.com/glider-ai/glider/internal/tools"
)

// Ref mirrors tools.Ref for older call sites.
type Ref = tools.Ref

// Result mirrors tools.Result.
type Result = tools.Result

// Registry is an alias for the unified tool registry.
type Registry = tools.Registry

// NewRegistry builds a default registry (workspace optional).
func NewRegistry(workspace string) *Registry {
	return tools.NewRegistry(tools.Options{Workspace: workspace})
}

// StubMCP kept for tests that imported nodetools.StubMCP — prefer mcp.Manager.
type StubMCP struct{}

func (StubMCP) ListTools(context.Context, string) ([]Ref, error) { return nil, nil }
func (StubMCP) Call(_ context.Context, server, tool, input string) (Result, error) {
	return Result{Name: tool, Kind: tools.KindMCP, OK: true, Stubbed: true,
		Output: "mcp stub server=" + server + " tool=" + tool}, nil
}

// EchoPluginID is the sample plugin id.
const EchoPluginID = "echo"

// InvokeAll is a package helper.
func InvokeAll(r *Registry, ctx context.Context, refs []Ref, input string) []Result {
	if r == nil {
		return nil
	}
	return r.InvokeAll(ctx, refs, input)
}
