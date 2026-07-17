package backend

import (
	"context"
	"time"
)

// --- Data Types ---

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Priority int

const (
	PriorityLow  Priority = 0
	PriorityHigh Priority = 1
)

type CompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Metadata    RequestMetadata `json:"-"`
}

type RequestMetadata struct {
	RequestID       string
	EstimatedTokens int
	Priority        Priority
	OriginalModel   string
	Adapter         string
}

type CompletionChunk struct {
	ID           string `json:"id"`
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason,omitempty"`
	Model        string `json:"model"`
}

type ExecutionStrategy string

const (
	StrategySingle   ExecutionStrategy = "single"
	StrategyFanOut   ExecutionStrategy = "fan_out"
	StrategyPipeline ExecutionStrategy = "pipeline"
	StrategyEnsemble ExecutionStrategy = "ensemble"
)

type SubTask struct {
	Prompt string
	Target string
	Model  string
}

type RoutingDecision struct {
	Strategy    ExecutionStrategy
	Target      string // "local" | "cloud"
	BackendName string
	Model       string
	Adapter     string
	RuleName    string
	Reason      string
	SubTasks    []SubTask
}

type BackendType string

const (
	BackendTypeLocal BackendType = "local"
	BackendTypeCloud BackendType = "cloud"
)

type LoadOptions struct {
	NumGPULayers int
	KeepAlive    time.Duration
	GPUIndex     int
}

type LoadedModel struct {
	Name      string
	SizeVRAM  int64
	SizeRAM   int64
	ExpiresAt time.Time
	Backend   string
}

type ModelState string

const (
	ModelStateCold      ModelState = "COLD"
	ModelStateLoading   ModelState = "LOADING"
	ModelStateWarm      ModelState = "WARM"
	ModelStateUnloading ModelState = "UNLOADING"
)

type ModelInfo struct {
	Name           string
	Backend        string
	VRAMEstimateMB int
	MaxContext     int
	Capabilities   []string
	Adapter        string
	KeepWarm       bool
	State          ModelState
}

// --- Interfaces ---

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
