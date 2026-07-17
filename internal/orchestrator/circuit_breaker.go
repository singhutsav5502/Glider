package orchestrator

import (
	"sync"
	"time"
)

// BreakerState is the circuit breaker phase.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

// CircuitBreaker skips backends after consecutive failures.
type CircuitBreaker struct {
	mu              sync.Mutex
	threshold       int
	cooldown        time.Duration
	failures        int
	state           BreakerState
	lastFailureTime time.Time
	halfOpenProbing bool
	now             func() time.Time
}

// NewCircuitBreaker creates a breaker with the given failure threshold and cooldown.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		state:     BreakerClosed,
		now:       time.Now,
	}
}

// Allow reports whether a request may be sent to the protected backend.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if cb.now().Sub(cb.lastFailureTime) >= cb.cooldown {
			cb.state = BreakerHalfOpen
			cb.halfOpenProbing = true
			return true
		}
		return false
	case BreakerHalfOpen:
		return cb.halfOpenProbing
	default:
		return false
	}
}

// RecordSuccess resets the breaker after a successful call.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = BreakerClosed
	cb.halfOpenProbing = false
}

// RecordFailure increments failures and may open the breaker.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailureTime = cb.now()
	if cb.state == BreakerHalfOpen {
		cb.state = BreakerOpen
		cb.halfOpenProbing = false
		return
	}
	if cb.failures >= cb.threshold {
		cb.state = BreakerOpen
	}
}

// State returns the current breaker phase (for tests).
func (cb *CircuitBreaker) State() BreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}
