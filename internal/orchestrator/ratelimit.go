package orchestrator

import (
	"sync"
	"time"
)

// CloudRateLimiter enforces requests-per-minute for cloud backends.
type CloudRateLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	requests []time.Time
	now      func() time.Time
}

// NewCloudRateLimiter creates a limiter with the given RPM.
func NewCloudRateLimiter(rpm int) *CloudRateLimiter {
	return &CloudRateLimiter{
		limit:  rpm,
		window: time.Minute,
		now:    time.Now,
	}
}

// Allow returns nil if a cloud request is permitted.
func (r *CloudRateLimiter) Allow() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	cutoff := now.Add(-r.window)
	alive := r.requests[:0]
	for _, t := range r.requests {
		if t.After(cutoff) {
			alive = append(alive, t)
		}
	}
	r.requests = alive

	if len(r.requests) >= r.limit {
		return &RateLimitExceededError{Limit: r.limit}
	}
	r.requests = append(r.requests, now)
	return nil
}

// BudgetTracker caps cumulative cloud spend.
type BudgetTracker struct {
	mu        sync.Mutex
	cap       float64
	spent     float64
	estimate  func(estimatedTokens int) float64
}

// NewBudgetTracker creates a budget tracker with the given monthly cap.
func NewBudgetTracker(cap float64, estimate func(estimatedTokens int) float64) *BudgetTracker {
	if estimate == nil {
		estimate = func(tokens int) float64 { return float64(tokens) * 0.00001 }
	}
	return &BudgetTracker{cap: cap, estimate: estimate}
}

// Allow checks whether a request fits within the remaining budget.
func (b *BudgetTracker) Allow(estimatedTokens int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cost := b.estimate(estimatedTokens)
	if b.spent+cost > b.cap {
		return &BudgetExceededError{Cap: b.cap, Spent: b.spent, Estimated: cost}
	}
	return nil
}

// RecordSpend adds actual cost after a successful cloud request.
func (b *BudgetTracker) RecordSpend(estimatedTokens int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += b.estimate(estimatedTokens)
}

// Spent returns cumulative spend (for tests).
func (b *BudgetTracker) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// SetSpent sets cumulative spend (for tests).
func (b *BudgetTracker) SetSpent(v float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent = v
}
