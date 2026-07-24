package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// HealthFunc reports whether a backend is healthy enough to receive traffic.
type HealthFunc func(name string) bool

// DefaultHealthCheck uses HealthChecker.IsHealthy when implemented.
func DefaultHealthCheck(registry *backend.Registry) HealthFunc {
	return func(name string) bool {
		b, err := registry.Get(name)
		if err != nil {
			return false
		}
		if hc, ok := b.(backend.HealthChecker); ok {
			return hc.IsHealthy()
		}
		return true
	}
}

type fallbackStep struct {
	backendName string
	model       string
	local       bool
}

// FallbackChain tries backends in order with circuit breaking and health checks.
type FallbackChain struct {
	registry             *backend.Registry
	lifecycle            *ModelLifecycle
	breakers             map[string]*CircuitBreaker
	failureThreshold     int
	cooldown             time.Duration
	isHealthy            HealthFunc
	rateLimiter          *CloudRateLimiter
	budget               *BudgetTracker
	cloudBackend         string
	cloudModel           string
	disableCloudFallback bool
}

// FallbackConfig configures the fallback chain.
type FallbackConfig struct {
	Registry         *backend.Registry
	Lifecycle        *ModelLifecycle
	FailureThreshold int
	Cooldown         time.Duration
	IsHealthy        HealthFunc
	RateLimiter      *CloudRateLimiter
	Budget           *BudgetTracker
	CloudBackend     string
	CloudModel       string
	// DisableCloudFallback skips appending BYOK cloud after a local primary (pure-local).
	DisableCloudFallback bool
}

// NewFallbackChain creates a fallback chain.
func NewFallbackChain(cfg FallbackConfig) *FallbackChain {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.IsHealthy == nil && cfg.Registry != nil {
		cfg.IsHealthy = DefaultHealthCheck(cfg.Registry)
	}
	return &FallbackChain{
		registry:             cfg.Registry,
		lifecycle:            cfg.Lifecycle,
		breakers:             make(map[string]*CircuitBreaker),
		failureThreshold:     cfg.FailureThreshold,
		cooldown:             cfg.Cooldown,
		isHealthy:            cfg.IsHealthy,
		rateLimiter:          cfg.RateLimiter,
		budget:               cfg.Budget,
		cloudBackend:         cfg.CloudBackend,
		cloudModel:           cfg.CloudModel,
		disableCloudFallback: cfg.DisableCloudFallback,
	}
}

func (f *FallbackChain) breaker(name string) *CircuitBreaker {
	if cb, ok := f.breakers[name]; ok {
		return cb
	}
	cb := NewCircuitBreaker(f.failureThreshold, f.cooldown)
	f.breakers[name] = cb
	return cb
}

func (f *FallbackChain) steps(decision *backend.RoutingDecision, req *backend.CompletionRequest) []fallbackStep {
	primary := fallbackStep{
		backendName: decision.BackendName,
		model:       decision.Model,
		local:       decision.Target != "cloud",
	}
	if primary.backendName == "" {
		if decision.Target == "cloud" {
			primary.backendName = f.cloudBackend
		} else {
			info, err := f.registry.GetModel(decision.Model)
			if err == nil {
				primary.backendName = info.Backend
			}
		}
	}
	if primary.model == "" {
		primary.model = req.Model
	}

	steps := []fallbackStep{primary}
	// Explicit /local|/fast is a hard force — never silently fall through to BYOK OpenAI.
	// (Hoop/swarm "local" route prefixes /local; a 401 from openai here was confusing.)
	explicitLocal := decision != nil && (strings.EqualFold(decision.RuleName, "Explicit Local") ||
		strings.Contains(strings.ToLower(decision.RuleName), "explicit local"))
	if primary.local && !f.disableCloudFallback && !explicitLocal && f.cloudBackend != "" {
		cloudModel := f.cloudModel
		if cloudModel == "" {
			cloudModel = req.Model
		}
		steps = append(steps, fallbackStep{
			backendName: f.cloudBackend,
			model:       cloudModel,
			local:       false,
		})
	}
	return steps
}

// reprobeLive runs HealthChecker.Ping when a backend looks unhealthy.
// Ollama/vLLM start with healthy=false until the first successful Ping; without
// this, local routes skip forever even when the daemon is up (no background poller).
func (f *FallbackChain) reprobeLive(ctx context.Context, name string) bool {
	if f == nil || f.registry == nil || name == "" {
		return false
	}
	b, err := f.registry.Get(name)
	if err != nil {
		return false
	}
	hc, ok := b.(backend.HealthChecker)
	if !ok {
		return true
	}
	if err := hc.Ping(ctx); err != nil {
		return false
	}
	return hc.IsHealthy()
}

// Execute runs the request through the fallback chain.
func (f *FallbackChain) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	var lastErr error
	var attemptErrs []string
	for _, step := range f.steps(decision, req) {
		if f.isHealthy != nil && !f.isHealthy(step.backendName) {
			if !f.reprobeLive(ctx, step.backendName) {
				msg := fmt.Sprintf("%s unhealthy", step.backendName)
				attemptErrs = append(attemptErrs, msg)
				if lastErr == nil {
					lastErr = fmt.Errorf("%s", msg)
				}
				continue
			}
		}

		cb := f.breaker(step.backendName)
		if !cb.Allow() {
			msg := fmt.Sprintf("%s circuit open", step.backendName)
			attemptErrs = append(attemptErrs, msg)
			if lastErr == nil {
				lastErr = fmt.Errorf("%s", msg)
			}
			continue
		}

		if !step.local {
			if f.rateLimiter != nil {
				if err := f.rateLimiter.Allow(); err != nil {
					return nil, err
				}
			}
			if f.budget != nil {
				tokens := req.Metadata.EstimatedTokens
				if err := f.budget.Allow(tokens); err != nil {
					return nil, err
				}
			}
		}

		attemptReq := *req
		attemptReq.Model = step.model

		if step.local && f.lifecycle != nil {
			if err := f.lifecycle.EnsureWarm(ctx, step.model); err != nil {
				cb.RecordFailure()
				attemptErrs = append(attemptErrs, fmt.Sprintf("%s warm: %v", step.backendName, err))
				lastErr = err
				continue
			}
			f.lifecycle.TouchWarm(step.model)
		}

		b, err := f.registry.Get(step.backendName)
		if err != nil {
			cb.RecordFailure()
			attemptErrs = append(attemptErrs, fmt.Sprintf("%s: %v", step.backendName, err))
			lastErr = err
			continue
		}

		ch, err := b.Complete(ctx, &attemptReq)
		if err != nil {
			cb.RecordFailure()
			attemptErrs = append(attemptErrs, fmt.Sprintf("%s: %v", step.backendName, err))
			lastErr = err
			continue
		}

		cb.RecordSuccess()
		if !step.local && f.budget != nil {
			f.budget.RecordSpend(req.Metadata.EstimatedTokens)
		}
		return ch, nil
	}

	if lastErr != nil {
		detail := lastErr.Error()
		if len(attemptErrs) > 0 {
			detail = strings.Join(attemptErrs, " | ")
		}
		return nil, &GatewayError{
			StatusCode: 502,
			Message:    fmt.Sprintf("all backends failed: %s", detail),
			Type:       "server_error",
		}
	}
	return nil, &GatewayError{
		StatusCode: 502,
		Message:    "all backends unavailable",
		Type:       "server_error",
	}
}

// BreakerFor returns the circuit breaker for a backend (tests).
func (f *FallbackChain) BreakerFor(name string) *CircuitBreaker {
	return f.breaker(name)
}
