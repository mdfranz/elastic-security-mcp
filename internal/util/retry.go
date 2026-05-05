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

// WithRetry executes the given function with exponential backoff on rate limit errors.
// It retries up to maxRetries times with exponential backoff before returning the error.
func WithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	const maxRetries = 5
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
		)

		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	res, err := fn()
	if err != nil && IsRateLimitError(err) {
		slog.Warn("Rate limit persists after retries",
			"attempts", maxRetries+1,
			"error", err,
		)
	}
	return res, err
}
