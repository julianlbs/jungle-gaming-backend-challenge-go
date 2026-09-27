package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
)

type PendingClaim struct {
	TransactionID wagering.ID
	WalletID      wallet.ID
}

// PendingQueue leases due operations; a claimed operation becomes due again after the lease.
type PendingQueue interface {
	ClaimDue(ctx context.Context, lease time.Duration, limit int) ([]PendingClaim, error)
}

type ResumeResult int

const (
	ResumeSkipped ResumeResult = iota
	ResumeRescheduled
	ResumeSettled
	ResumeFailed
)

func (r ResumeResult) String() string {
	switch r {
	case ResumeRescheduled:
		return "rescheduled"
	case ResumeSettled:
		return "settled"
	case ResumeFailed:
		return "failed"
	default:
		return "skipped"
	}
}

// apiFailureDetail is the stable, non-sensitive text persisted and returned on GET for
// PROCESSING_FAILED. The real cause stays in the process log.
const apiFailureDetail = "processing failed"

type PendingResumer struct {
	uow    UnitOfWork
	queue  PendingQueue
	clock  Clock
	ids    IDGenerator
	policy PendingPolicy
	nudger ReferenceNudger
	log    *slog.Logger
}

func NewPendingResumer(uow UnitOfWork, queue PendingQueue, clock Clock, ids IDGenerator, policy PendingPolicy, nudger ReferenceNudger, log *slog.Logger) *PendingResumer {
	if log == nil {
		log = slog.Default()
	}
	return &PendingResumer{uow: uow, queue: queue, clock: clock, ids: ids, policy: policy, nudger: nudger, log: log}
}

// RunOnce claims a batch of due operations and resumes each in its own transaction. Transient
// failures leave the lease to expire so that any instance can retry.
func (r *PendingResumer) RunOnce(ctx context.Context, lease time.Duration, batch int) (map[ResumeResult]int, error) {
	claims, err := r.queue.ClaimDue(ctx, lease, batch)
	if err != nil {
		return nil, err
	}
	results := map[ResumeResult]int{}
	var errs []error
	for _, c := range claims {
		if ctx.Err() != nil {
			break
		}
		res, err := r.Resume(ctx, c)
		if err != nil {
			errs = append(errs, fmt.Errorf("resume %s: %w", c.TransactionID, err))
			continue
		}
		results[res]++
	}
	return results, errors.Join(errs...)
}

func (r *PendingResumer) Resume(ctx context.Context, c PendingClaim) (res ResumeResult, err error) {
	ctx, span := tracer.Start(ctx, "pending.resume")
	defer func() {
		span.SetAttributes(attribute.String("pending.result", res.String()))
		endSpan(span, err)
	}()
	var (
		result  ResumeResult
		settled *wagering.Transaction
	)
	err = r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		result, settled = ResumeSkipped, nil
		// Lock order matches the processor: wallet first, then the transaction.
		w, err := tx.Wallets().GetForUpdate(ctx, c.WalletID)
		if err != nil {
			return err
		}
		t, err := tx.Transactions().GetForUpdate(ctx, c.TransactionID)
		if err != nil {
			return err
		}
		if t.Status() != wagering.StatusPendingReference {
			return nil
		}

		now := r.clock.Now()
		ref, reversed, err := loadReference(ctx, tx, t)
		if err != nil {
			return err
		}
		eval := wagering.EvaluateReference(t, ref, reversed)

		var mv *wallet.Movement
		switch {
		case eval.Decision != wagering.DecisionWait:
			if err := t.Resume(now); err != nil {
				return err
			}
			if mv, err = settle(t, w, ref, eval, now); err != nil {
				return err
			}
		case t.ReferenceWaitExhausted(now, r.policy.MaxAttempts):
			if err := t.Resume(now); err != nil {
				return err
			}
			current := wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}
			if err := t.Reject(wagering.FailureReferenceNotFound, current, nil, now); err != nil {
				return err
			}
		default:
			if err := t.Reschedule(now.Add(r.policy.Delay(t.Attempts())), now); err != nil {
				return err
			}
			result = ResumeRescheduled
			return tx.Transactions().Update(ctx, t)
		}

		if err := tx.Transactions().Update(ctx, t); err != nil {
			return err
		}
		if err := persistEffects(ctx, tx, r.ids, t, w, mv, ""); err != nil {
			return err
		}
		result, settled = ResumeSettled, t
		return nil
	})
	if err == nil {
		if settled != nil && settled.Status() == wagering.StatusProcessed && r.nudger != nil {
			if ext, ok := settled.External(); ok {
				_ = r.nudger.NudgeWaiting(context.WithoutCancel(ctx), ext.ProviderID, ext.ExternalID)
			}
		}
		return result, nil
	}
	if errors.Is(err, ErrUnavailable) || ctx.Err() != nil {
		return ResumeSkipped, err
	}
	if failErr := r.fail(ctx, c, err); failErr != nil {
		return ResumeSkipped, errors.Join(err, failErr)
	}
	return ResumeFailed, nil
}

// fail records a permanent processing error so that the operation stops being retried and
// remains auditable. The API-visible detail is fixed; the cause is only logged.
func (r *PendingResumer) fail(ctx context.Context, c PendingClaim, cause error) error {
	r.log.ErrorContext(logging.With(ctx, logging.KeyTransactionID, c.TransactionID.String()),
		"pending resume failed permanently", "error", cause, logging.KeyWalletID, c.WalletID.String())
	return r.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		t, err := tx.Transactions().GetForUpdate(ctx, c.TransactionID)
		if err != nil {
			return err
		}
		if t.Status() != wagering.StatusPendingReference {
			return nil
		}
		if err := t.Fail(apiFailureDetail, r.clock.Now()); err != nil {
			return err
		}
		return tx.Transactions().Update(ctx, t)
	})
}
