package backend

import "context"

// InferenceBackend executes completions.
type InferenceBackend interface {
	Name() string
	Type() BackendType
	Complete(ctx context.Context, req *CompletionRequest) (<-chan CompletionChunk, error)
}

// ModelManager manages model lifecycle on a backend.
type ModelManager interface {
	LoadModel(ctx context.Context, model string, opts LoadOptions) error
	UnloadModel(ctx context.Context, model string) error
	ListLoaded(ctx context.Context) ([]LoadedModel, error)
}

// LoRAManager manages adapter hot-swapping (vLLM only).
type LoRAManager interface {
	LoadAdapter(ctx context.Context, name string, path string) error
	UnloadAdapter(ctx context.Context, name string) error
	ListAdapters(ctx context.Context) ([]string, error)
}

// HealthChecker reports backend health.
type HealthChecker interface {
	Ping(ctx context.Context) error
	IsHealthy() bool
}
