package orchestrator

import "fmt"

// GatewayError represents an upstream failure surfaced to the caller (HTTP 502).
type GatewayError struct {
	StatusCode int
	Message    string
	Type       string
}

func (e *GatewayError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("gateway error (status %d)", e.StatusCode)
}

// RateLimitExceededError is returned when cloud RPM budget is exhausted.
type RateLimitExceededError struct {
	Limit int
}

func (e *RateLimitExceededError) Error() string {
	return fmt.Sprintf("cloud rate limit exceeded (%d requests/minute)", e.Limit)
}

// BudgetExceededError is returned when a request would exceed the cloud budget cap.
type BudgetExceededError struct {
	Cap       float64
	Spent     float64
	Estimated float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("cloud budget exceeded: cap $%.2f, spent $%.2f, request estimated $%.2f",
		e.Cap, e.Spent, e.Estimated)
}
