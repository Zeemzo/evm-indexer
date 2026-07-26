// Package retry provides context-aware retries with exponential backoff
// and jitter, for wrapping flaky RPC calls.
package retry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

type Options struct {
	// MaxAttempts is the total number of tries, including the first one.
	MaxAttempts int
	// BaseDelay is the delay after the first failure; it doubles each
	// attempt up to MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// OnRetry, if set, is called before each sleep with the attempt
	// number (1-based) and the error that triggered the retry.
	OnRetry func(attempt int, err error)
}

func DefaultOptions() Options {
	return Options{
		MaxAttempts: 4,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    15 * time.Second,
	}
}

// Do runs fn until it succeeds, attempts are exhausted, or ctx is done.
// The last error is wrapped with attempt information.
func Do[T any](ctx context.Context, opts Options, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}

	var lastErr error
	delay := opts.BaseDelay
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if attempt == opts.MaxAttempts {
			break
		}
		if opts.OnRetry != nil {
			opts.OnRetry(attempt, err)
		}

		// Full jitter: sleep a random duration in (0, delay] so many
		// callers backing off together don't re-stampede the RPC node.
		sleep := time.Duration(rand.Int64N(int64(delay))) + time.Millisecond
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(sleep):
		}

		delay *= 2
		if opts.MaxDelay > 0 && delay > opts.MaxDelay {
			delay = opts.MaxDelay
		}
	}
	return zero, fmt.Errorf("all %d attempts failed: %w", opts.MaxAttempts, lastErr)
}
