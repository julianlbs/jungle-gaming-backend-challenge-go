package postgres

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
)

type RetryPolicy struct {
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// OnRetry, when set, is called before each new attempt.
	OnRetry func(reason string)
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Attempts: 4, BaseDelay: 20 * time.Millisecond, MaxDelay: 500 * time.Millisecond}
}

// do runs fn until it succeeds, fails permanently or the attempts run out. Exhausted
// transient failures are reported as app.ErrUnavailable.
func (p RetryPolicy) do(ctx context.Context, fn func(context.Context) error) error {
	attempts := max(p.Attempts, 1)
	var lastErr error
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		retryable, reason := isRetryable(err)
		if !retryable {
			return err
		}
		lastErr = err
		if attempt >= attempts {
			break
		}
		if p.OnRetry != nil {
			p.OnRetry(reason)
		}
		timer := time.NewTimer(p.backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", app.ErrUnavailable, ctx.Err())
		case <-timer.C:
		}
	}
	return fmt.Errorf("%w: %w", app.ErrUnavailable, lastErr)
}

// backoff is exponential with full jitter.
func (p RetryPolicy) backoff(attempt int) time.Duration {
	base := max(p.BaseDelay, time.Millisecond)
	ceiling := max(p.MaxDelay, base)
	d := base << min(attempt-1, 20)
	if d <= 0 || d > ceiling {
		d = ceiling
	}
	return rand.N(d) + 1
}
