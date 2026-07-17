package orchestrator

import (
	"fmt"
	"net/http"

	"github.com/glider-ai/glider/internal/backend"
)

// PassthroughCompleter routes all requests to a single named backend (Phase 1).
type PassthroughCompleter struct {
	Registry    *backend.Registry
	BackendName string
	Model       string
}

func (p *PassthroughCompleter) Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	name := p.BackendName
	if name == "" {
		name = "ollama"
	}
	b, err := p.Registry.Get(name)
	if err != nil {
		return nil, err
	}
	if p.Model != "" {
		req.Model = p.Model
	}
	if req.Model == "" {
		return nil, fmt.Errorf("no model specified")
	}
	return b.Complete(r.Context(), req)
}

// RegistryModelLister adapts Registry for /v1/models.
type RegistryModelLister struct {
	Registry *backend.Registry
}

func (l RegistryModelLister) ListModelIDs() []string {
	models := l.Registry.ListModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.Name)
	}
	return ids
}
