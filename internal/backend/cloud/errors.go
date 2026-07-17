package cloud

import (
	"fmt"
	"time"
)

// RateLimitError is returned when a cloud API responds with HTTP 429.
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (status %d, retry-after %s): %s", e.StatusCode, e.RetryAfter, e.Message)
}

func IsRateLimit(err error) bool {
	_, ok := err.(*RateLimitError)
	return ok
}
