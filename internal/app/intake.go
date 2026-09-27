package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

// ErrMessageConflict means a message id was delivered again with a different content.
var ErrMessageConflict = errors.New("message id reused with a different payload")

// IncomingWager is a broker message already decoded into a command.
type IncomingWager struct {
	Consumer  string
	MessageID string
	Command   WagerCommand
}

type IntakeResult struct {
	Outcome WagerOutcome
	// Duplicate is set when the message had already been handled; Outcome is then empty.
	Duplicate bool
	Entry     InboxEntry
}

// WagerIntake handles broker messages, recording each one in the inbox in the same transaction
// as its financial effects so that redeliveries never apply an operation twice.
type WagerIntake struct {
	uow       UnitOfWork
	processor *WagerProcessor
	clock     Clock
}

func NewWagerIntake(uow UnitOfWork, processor *WagerProcessor, clock Clock) *WagerIntake {
	return &WagerIntake{uow: uow, processor: processor, clock: clock}
}

func (i *WagerIntake) Handle(ctx context.Context, msg IncomingWager) (IntakeResult, error) {
	received := i.clock.Now()
	hash := MessageHash(msg.Command)
	var res IntakeResult
	err := i.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		res = IntakeResult{}
		prev, err := tx.Inbox().Find(ctx, msg.Consumer, msg.MessageID)
		switch {
		case err == nil:
			if prev.PayloadHash != hash {
				return ErrMessageConflict
			}
			res = IntakeResult{Duplicate: true, Entry: prev}
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}

		out, err := i.processor.Execute(ctx, tx, msg.Command)
		if err != nil {
			return err
		}
		entry := InboxEntry{
			Consumer:      msg.Consumer,
			MessageID:     msg.MessageID,
			PayloadHash:   hash,
			TransactionID: out.Transaction.ID().UUID(),
			Outcome:       inboxOutcome(out),
			ReceivedAt:    received,
			CompletedAt:   i.clock.Now(),
		}
		if err := tx.Inbox().Insert(ctx, entry); err != nil {
			return err
		}
		res = IntakeResult{Outcome: out, Entry: entry}
		return nil
	})
	if err != nil {
		return IntakeResult{}, err
	}
	if !res.Duplicate {
		i.processor.AfterCommit(ctx, res.Outcome)
	}
	return res, nil
}

func inboxOutcome(out WagerOutcome) InboxOutcome {
	if out.Replay {
		return InboxReplayed
	}
	switch out.Transaction.Status() {
	case wagering.StatusRejected:
		return InboxRejected
	case wagering.StatusPendingReference, wagering.StatusPending:
		return InboxPendingReference
	default:
		return InboxProcessed
	}
}

// MessageHash identifies the content of a message: every command field supplied by the
// producer, including the idempotency key, and none of the transport metadata.
func MessageHash(c WagerCommand) string {
	doc, _ := json.Marshal(map[string]string{
		"providerId":                     c.ProviderID,
		"externalTransactionId":          c.ExternalTransactionID,
		"idempotencyKey":                 c.IdempotencyKey,
		"walletId":                       c.WalletID,
		"playerId":                       c.PlayerID,
		"kind":                           c.Kind,
		"amount":                         c.Amount,
		"currency":                       c.Currency,
		"roundId":                        c.RoundID,
		"gameId":                         c.GameID,
		"referenceExternalTransactionId": c.ReferenceExternalTransactionID,
	})
	sum := sha256.Sum256(doc)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsPermanent reports whether retrying err can never succeed without changing the input.
func IsPermanent(err error) bool {
	var fe *wagering.FieldError
	return errors.As(err, &fe) ||
		errors.Is(err, wagering.ErrReservedKind) ||
		errors.Is(err, ErrWalletNotFound) ||
		errors.Is(err, ErrWalletMismatch) ||
		errors.Is(err, ErrIdempotencyKeyReused) ||
		errors.Is(err, ErrExternalTransactionConflict) ||
		errors.Is(err, ErrMessageConflict)
}
