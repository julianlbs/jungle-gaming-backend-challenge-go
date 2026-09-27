package app

import (
	"fmt"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/event"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// outcomeEvents builds the integration events for a transaction that reached a
// reportable state, plus the balance change when a movement was applied.
func outcomeEvents(ids IDGenerator, t *wagering.Transaction, mv *wallet.Movement, causationID string) ([]event.Outgoing, error) {
	meta := func() event.Meta {
		return event.Meta{
			EventID:       ids.New(),
			CorrelationID: t.CorrelationID(),
			CausationID:   causationID,
			OccurredAt:    t.UpdatedAt(),
		}
	}

	var out []event.Outgoing
	add := func(o event.Outgoing, err error) error {
		if err != nil {
			return err
		}
		out = append(out, o)
		return nil
	}

	var err error
	switch t.Status() {
	case wagering.StatusProcessed:
		err = add(encode(event.NewWagerTransactionProcessed(meta(), t)))
	case wagering.StatusRejected:
		err = add(encode(event.NewWagerTransactionRejected(meta(), t)))
	case wagering.StatusPendingReference:
		err = add(encode(event.NewWagerTransactionPendingReference(meta(), t)))
	default:
		return nil, fmt.Errorf("no event for status %s", t.Status())
	}
	if err != nil {
		return nil, err
	}
	if mv != nil {
		if err := add(encode(event.NewWalletBalanceChanged(meta(), t.WalletID(), t.ID(), *mv))); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func encode[T any](env event.Envelope[T], err error) (event.Outgoing, error) {
	if err != nil {
		return event.Outgoing{}, err
	}
	return env.Encode()
}
