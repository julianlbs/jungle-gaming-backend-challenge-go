package wiring

import (
	"context"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/sqsconsumer"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/fault"
)

// These wrappers mark the crash points exercised by the end-to-end tests; without the
// faultinject build tag fault.Hit does nothing.

type faultyHandler struct{ inner sqsconsumer.Handler }

func (h faultyHandler) Handle(ctx context.Context, msg app.IncomingWager) (app.IntakeResult, error) {
	res, err := h.inner.Handle(ctx, msg)
	if err == nil {
		fault.Hit(fault.ConsumerAfterCommit)
	}
	return res, err
}

type faultyPublisher struct{ inner app.EventPublisher }

func (p faultyPublisher) Publish(ctx context.Context, ev app.ClaimedEvent) error {
	if err := p.inner.Publish(ctx, ev); err != nil {
		return err
	}
	fault.Hit(fault.OutboxAfterPublish)
	return nil
}

type faultyPendingQueue struct{ inner app.PendingQueue }

func (q faultyPendingQueue) ClaimDue(ctx context.Context, lease time.Duration, limit int) ([]app.PendingClaim, error) {
	claims, err := q.inner.ClaimDue(ctx, lease, limit)
	if err == nil && len(claims) > 0 {
		fault.Hit(fault.PendingAfterClaim)
	}
	return claims, err
}
