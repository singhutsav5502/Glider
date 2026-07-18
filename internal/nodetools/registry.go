// Package nodetools is DEPRECATED — use github.com/glider-ai/glider/internal/tools.
// This package remains as thin type aliases only; StubMCP has been removed.
package nodetools

import (
	"context"

	"github.com/glider-ai/glider/internal/tools"
)

// Ref mirrors tools.Ref.
type Ref = tools.Ref

// Result mirrors tools.Result.
type Result = tools.Result

// Registry is an alias for the unified tool registry.
type Registry = tools.Registry

// NewRegistry builds a default registry (workspace optional).
func NewRegistry(workspace string) *Registry {
	return tools.NewRegistry(tools.Options{Workspace: workspace})
}

// InvokeAll is a package helper.
func InvokeAll(r *Registry, ctx context.Context, refs []Ref, input string) []Result {
	if r == nil {
		return nil
	}
	return r.InvokeAll(ctx, refs, input)
}
