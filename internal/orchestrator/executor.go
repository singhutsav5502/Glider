package orchestrator

import (
	"context"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// Executor coordinates request execution for a routing decision.
type Executor interface {
	Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

// SimpleExecutorConfig configures the V1 single-strategy executor.
type SimpleExecutorConfig struct {
	Registry         *backend.Registry
	VRAM             VRAMManager
	IdleUnload       time.Duration
	FailureThreshold int
	BreakerCooldown  time.Duration
	CloudBackend     string
	CloudModel       string
	// DisableCloudFallback skips BYOK cloud after local (pure-local profiles).
	DisableCloudFallback bool
	RateLimiter      *CloudRateLimiter
	Budget           *BudgetTracker
	IsHealthy        HealthFunc
}

// SimpleExecutor handles StrategySingle with lifecycle, fallback, and guards.
type SimpleExecutor struct {
	registry  *backend.Registry
	lifecycle *ModelLifecycle
	fallback  *FallbackChain
	queue     *PriorityQueue
}

var _ Executor = (*SimpleExecutor)(nil)

// NewSimpleExecutor creates a SimpleExecutor wired with lifecycle and fallback.
func NewSimpleExecutor(cfg SimpleExecutorConfig) *SimpleExecutor {
	if cfg.IdleUnload <= 0 {
		cfg.IdleUnload = 5 * time.Minute
	}
	lifecycle := NewModelLifecycle(cfg.Registry, cfg.VRAM, cfg.IdleUnload)
	fallback := NewFallbackChain(FallbackConfig{
		Registry:             cfg.Registry,
		Lifecycle:            lifecycle,
		FailureThreshold:     cfg.FailureThreshold,
		Cooldown:             cfg.BreakerCooldown,
		IsHealthy:            cfg.IsHealthy,
		RateLimiter:          cfg.RateLimiter,
		Budget:               cfg.Budget,
		CloudBackend:         cfg.CloudBackend,
		CloudModel:           cfg.CloudModel,
		DisableCloudFallback: cfg.DisableCloudFallback,
	})
	e := &SimpleExecutor{
		registry:  cfg.Registry,
		lifecycle: lifecycle,
		fallback:  fallback,
		queue:     NewPriorityQueue(),
	}
	go e.runQueueWorker()
	return e
}

func (e *SimpleExecutor) runQueueWorker() {
	for {
		_, _ = e.queue.Dequeue(context.Background())
	}
}

// Execute enqueues by priority then runs the fallback chain.
func (e *SimpleExecutor) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	priority := req.Metadata.Priority
	if err := e.queue.Enqueue(ctx, priority); err != nil {
		return nil, err
	}
	return e.fallback.Execute(ctx, decision, req)
}

// Lifecycle exposes the model lifecycle manager (tests).
func (e *SimpleExecutor) Lifecycle() *ModelLifecycle {
	return e.lifecycle
}

// Fallback exposes the fallback chain (tests).
func (e *SimpleExecutor) Fallback() *FallbackChain {
	return e.fallback
}

// Queue exposes the priority queue (tests).
func (e *SimpleExecutor) Queue() *PriorityQueue {
	return e.queue
}
