package app

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

type OutboxQueue interface {
	Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]ClaimedEvent, error)
	MarkPublished(ctx context.Context, id uuid.UUID) (bool, error)
	Reschedule(ctx context.Context, id uuid.UUID, owner string, delay time.Duration, cause error) error
	ReleaseLeases(ctx context.Context, owner string) (int64, error)
}

type EventPublisher interface {
	Publish(ctx context.Context, ev ClaimedEvent) error
}

type RelayConfig struct {
	// Owner identifies this instance in leases; it must be unique among running publishers.
	Owner     string
	Lease     time.Duration
	BatchSize int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

type RelayResult string

const (
	RelayPublished   RelayResult = "published"
	RelayFailed      RelayResult = "failed"
	RelayLeaseLost   RelayResult = "lease_lost"
	RelayRescheduled RelayResult = "reschedule_failed"
)

// OutboxRelay publishes committed events. Delivery is at least once: an event is marked only
// after the broker accepted it, and is never given up on.
type OutboxRelay struct {
	queue     OutboxQueue
	publisher EventPublisher
	cfg       RelayConfig
	observe   func(RelayResult, ClaimedEvent, error)
}

func NewOutboxRelay(queue OutboxQueue, publisher EventPublisher, cfg RelayConfig, observe func(RelayResult, ClaimedEvent, error)) *OutboxRelay {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = time.Second
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Minute
	}
	if observe == nil {
		observe = func(RelayResult, ClaimedEvent, error) {}
	}
	return &OutboxRelay{queue: queue, publisher: publisher, cfg: cfg, observe: observe}
}

// RunOnce publishes one batch and reports how many events were claimed.
func (r *OutboxRelay) RunOnce(ctx context.Context) (int, error) {
	batch, err := r.queue.Claim(ctx, r.cfg.Owner, r.cfg.Lease, r.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	for _, ev := range batch {
		if ctx.Err() != nil {
			break
		}
		r.publish(ctx, ev)
	}
	return len(batch), nil
}

func (r *OutboxRelay) publish(ctx context.Context, ev ClaimedEvent) {
	if err := r.publisher.Publish(ctx, ev); err != nil {
		if rerr := r.queue.Reschedule(context.WithoutCancel(ctx), ev.ID, r.cfg.Owner, r.Backoff(ev.Attempts), err); rerr != nil {
			// The lease expires on its own and another publisher retries.
			r.observe(RelayRescheduled, ev, rerr)
			return
		}
		r.observe(RelayFailed, ev, err)
		return
	}
	marked, err := r.queue.MarkPublished(context.WithoutCancel(ctx), ev.ID)
	switch {
	case err != nil:
		// Published but unmarked: the event is republished later with the same id.
		r.observe(RelayFailed, ev, err)
	case !marked:
		r.observe(RelayLeaseLost, ev, nil)
	default:
		r.observe(RelayPublished, ev, nil)
	}
}

// Drain publishes batches until the backlog is empty or ctx ends.
func (r *OutboxRelay) Drain(ctx context.Context) error {
	for ctx.Err() == nil {
		n, err := r.RunOnce(ctx)
		if err != nil {
			return err
		}
		if n < r.cfg.BatchSize {
			return nil
		}
	}
	return nil
}

// Release hands unpublished leases back so that other instances can claim them at once.
func (r *OutboxRelay) Release(ctx context.Context) (int64, error) {
	return r.queue.ReleaseLeases(ctx, r.cfg.Owner)
}

// Backoff is the delay before the next attempt after the given number of claims.
func (r *OutboxRelay) Backoff(attempts int) time.Duration {
	d := r.cfg.MaxDelay
	if attempts < 30 {
		d = min(r.cfg.BaseDelay<<max(attempts-1, 0), r.cfg.MaxDelay)
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}
