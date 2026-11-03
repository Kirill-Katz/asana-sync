package internal

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// RetryConfig controls retries for each page independently.
type RetryConfig struct {
	MaxAttempts int           // Includes the initial attempt; must be at least 1.
	MaxBackoff  time.Duration // Caps exponential backoff, before jitter; at least 1s.
	MaxJitter   time.Duration // Inclusive upper bound of additive jitter; non-negative.
}

func (c RetryConfig) validate() error {
	if c.MaxAttempts < 1 {
		return errors.New("MaxAttempts must be at least 1")
	}
	if c.MaxBackoff < time.Second {
		return errors.New("MaxBackoff must be at least 1 second")
	}
	if c.MaxJitter < 0 || c.MaxBackoff > time.Duration(math.MaxInt64)-c.MaxJitter {
		return errors.New("MaxJitter must be non-negative and MaxBackoff + MaxJitter must fit in time.Duration")
	}
	return nil
}

// retryableError marks transient failures. A negative retryAfter means no
// usable Retry-After header was supplied, so exponential backoff applies.
type retryableError struct {
	err        error
	retryAfter time.Duration
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func fetchWithRetries[T any](ctx context.Context, c *AsanaAPIClient, resource string, fetch func() (T, error)) (T, error) {
	var zero T
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		page, err := fetch()
		if err == nil {
			return page, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		c.logger.ErrorContext(ctx, "Page fetch failed", "resource", resource, "attempt", attempt, "error", err)

		var transient *retryableError
		if attempt >= c.retry.MaxAttempts || !errors.As(err, &transient) {
			return zero, err
		}

		delay := transient.retryAfter
		if delay < 0 {
			delay = retryDelay(backoff, c.retry.MaxJitter)
		}
		c.logger.InfoContext(ctx, "Retrying page fetch",
			"resource", resource,
			"attempt", attempt+1,
			"max_attempts", c.retry.MaxAttempts,
			"delay", delay,
			"retry_after", transient.retryAfter >= 0,
		)
		if err := c.waitRetry(ctx, delay); err != nil {
			return zero, err
		}
		backoff = nextBackoff(backoff, c.retry.MaxBackoff)
	}
}

func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
		return -1
	}
	return time.Duration(seconds) * time.Second
}

func retryDelay(backoff, maxJitter time.Duration) time.Duration {
	return backoff + time.Duration(rand.Uint64N(uint64(maxJitter)+1))
}

func nextBackoff(backoff, maximum time.Duration) time.Duration {
	if backoff >= maximum-backoff {
		return maximum
	}
	return backoff * 2
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
