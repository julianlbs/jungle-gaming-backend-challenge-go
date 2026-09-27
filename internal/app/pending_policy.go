package app

import (
	"math/rand/v2"
	"time"
)

// PendingPolicy bounds how long an operation waits for its reference to arrive.
type PendingPolicy struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	MaxAttempts int
	TTL         time.Duration
	// Jitter is the relative spread applied to each delay, e.g. 0.2 for ±20%.
	Jitter float64
}

func DefaultPendingPolicy() PendingPolicy {
	return PendingPolicy{
		BaseDelay:   2 * time.Second,
		MaxDelay:    5 * time.Minute,
		MaxAttempts: 10,
		TTL:         30 * time.Minute,
		Jitter:      0.2,
	}
}

// Delay returns the wait before the next resolution attempt after the given number of attempts.
func (p PendingPolicy) Delay(attempts int) time.Duration {
	base := max(p.BaseDelay, time.Millisecond)
	ceiling := max(p.MaxDelay, base)
	d := base << min(max(attempts, 0), 30)
	if d <= 0 || d > ceiling {
		d = ceiling
	}
	if p.Jitter > 0 {
		spread := float64(d) * min(p.Jitter, 1)
		d += time.Duration((rand.Float64()*2 - 1) * spread)
	}
	return max(d, time.Millisecond)
}
