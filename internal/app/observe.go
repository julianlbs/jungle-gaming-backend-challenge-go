package app

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

// Wager results that did not produce a transaction status.
const (
	ResultInvalid     = "INVALID"
	ResultConflict    = "IDEMPOTENCY_CONFLICT"
	ResultUnavailable = "UNAVAILABLE"
	ResultConcurrent  = "CONCURRENT_UPDATE"
	ResultDuplicate   = "DUPLICATE"
	ResultError       = "ERROR"
)

// Idempotency conflict reasons.
const (
	ConflictKeyReused     = "idempotency_key_reused"
	ConflictExternalID    = "external_transaction_id"
	ConflictMessageReused = "message_id_reused"
	unknownKind           = "UNKNOWN"
)

// WagerObservation describes one handled wager with bounded label values only.
type WagerObservation struct {
	Channel string
	Kind    string
	// Status is the transaction status, or one of the Result constants when there is none.
	Status   string
	Replay   bool
	Conflict string
	// ConcurrencyConflict is set when a concurrent writer made the operation fail.
	ConcurrencyConflict bool
	Duration            time.Duration
}

type WagerObserver func(WagerObservation)

func (p *WagerProcessor) observe(ctx context.Context, cmd WagerCommand, out WagerOutcome, duplicate bool, err error, start time.Time) {
	o := observation(cmd, out, duplicate, err, time.Since(start))
	annotateWagerSpan(trace.SpanFromContext(ctx), o, err)
	if p.observer != nil {
		p.observer(o)
	}
}

func observation(cmd WagerCommand, out WagerOutcome, duplicate bool, err error, d time.Duration) WagerObservation {
	o := WagerObservation{Channel: string(cmd.Channel), Kind: unknownKind, Duration: d}
	switch cmd.Channel {
	case wagering.ChannelHTTP, wagering.ChannelSQS:
	default:
		o.Channel = unknownKind
	}
	if k, kerr := wagering.ParseExternalKind(cmd.Kind); kerr == nil {
		o.Kind = string(k)
	}
	switch {
	case duplicate:
		o.Status, o.Replay = ResultDuplicate, true
	case err == nil:
		o.Status, o.Replay = string(out.Transaction.Status()), out.Replay
	case errors.Is(err, ErrIdempotencyKeyReused):
		o.Status, o.Conflict = ResultConflict, ConflictKeyReused
	case errors.Is(err, ErrExternalTransactionConflict):
		o.Status, o.Conflict = ResultConflict, ConflictExternalID
	case errors.Is(err, ErrMessageConflict):
		o.Status, o.Conflict = ResultConflict, ConflictMessageReused
	case errors.Is(err, ErrConcurrentUpdate):
		o.Status, o.ConcurrencyConflict = ResultConcurrent, true
	case errors.Is(err, ErrUnavailable):
		o.Status = ResultUnavailable
	case IsPermanent(err):
		o.Status = ResultInvalid
	default:
		o.Status = ResultError
	}
	return o
}
