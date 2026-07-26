package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastOptions(maxAttempts int) Options {
	return Options{MaxAttempts: maxAttempts, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
}

func TestDoSucceedsFirstAttempt(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), fastOptions(3), func(ctx context.Context) (int, error) {
		calls++
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, result)
	assert.Equal(t, 1, calls)
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), fastOptions(3), func(ctx context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("transient")
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 3, calls)
}

func TestDoExhaustsAttempts(t *testing.T) {
	sentinel := errors.New("permanent failure")
	calls := 0
	_, err := Do(context.Background(), fastOptions(3), func(ctx context.Context) (int, error) {
		calls++
		return 0, sentinel
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls)
	assert.ErrorIs(t, err, sentinel, "original error must be preserved for errors.Is")
}

func TestDoRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	opts := Options{MaxAttempts: 10, BaseDelay: 50 * time.Millisecond}
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := Do(ctx, opts, func(ctx context.Context) (int, error) {
		calls++
		return 0, errors.New("fail")
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, calls, 10, "should stop retrying once context is cancelled")
}

func TestDoOnRetryCallback(t *testing.T) {
	var attempts []int
	opts := fastOptions(3)
	opts.OnRetry = func(attempt int, err error) { attempts = append(attempts, attempt) }
	_, _ = Do(context.Background(), opts, func(ctx context.Context) (int, error) {
		return 0, errors.New("fail")
	})
	assert.Equal(t, []int{1, 2}, attempts, "OnRetry fires before every sleep, not after the final failure")
}
