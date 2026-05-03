package util

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// IsRateLimitError checks if the error is a rate limit error (429).
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") || 
		strings.Contains(msg, "rate limit") || 
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "overloaded")
}

// WithRetry executes the given function with exponential backoff if it returns a rate limit error.
func WithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	maxRetries := 5
	backoff := 2 * time.Second

	for i := 0; i < maxRetries; i++ {
		res, err := fn()
		if err == nil {
			return res, nil
		}

		if !IsRateLimitError(err) {
			return res, err
		}

		slog.Warn("Rate limit hit, retrying...", 
			"attempt", i+1, 
			"max_retries", maxRetries, 
			"backoff", backoff, 
			"error", err,
		)

		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return fn() // Final attempt
}
