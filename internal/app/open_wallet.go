package app

import (
	"context"
	"fmt"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type OpenWalletCommand struct {
	PlayerID      string
	Currency      string
	InitialAmount string
	CorrelationID string
}

type WalletOpener struct {
	uow   UnitOfWork
	clock Clock
	ids   IDGenerator
}

func NewWalletOpener(uow UnitOfWork, clock Clock, ids IDGenerator) *WalletOpener {
	return &WalletOpener{uow: uow, clock: clock, ids: ids}
}

// Open creates the wallet and, for a positive initial balance, records it as an
// OPENING transaction with its ledger entry and events in the same commit.
func (o *WalletOpener) Open(ctx context.Context, cmd OpenWalletCommand) (*wallet.Wallet, error) {
	player, err := wallet.ParsePlayerID(cmd.PlayerID)
	if err != nil {
		return nil, &wagering.FieldError{Field: "playerId", Reason: "must be a UUID"}
	}
	currency, err := money.ParseCurrency(cmd.Currency)
	if err != nil {
		return nil, &wagering.FieldError{Field: "currency", Reason: "unsupported currency"}
	}
	amount := cmd.InitialAmount
	if amount == "" {
		amount = "0"
	}
	initial, err := money.Parse(amount, currency)
	if err != nil {
		return nil, &wagering.FieldError{Field: "initialBalance.amount", Reason: "must be a non-negative decimal with up to 2 places"}
	}
	if cmd.CorrelationID == "" {
		return nil, &wagering.FieldError{Field: "correlationId", Reason: "is required"}
	}

	var opened *wallet.Wallet
	err = o.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		now := o.clock.Now()
		id, err := wallet.NewID(o.ids.New())
		if err != nil {
			return err
		}
		w, mv, err := wallet.Open(id, player, initial, now)
		if err != nil {
			return err
		}
		if err := tx.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		if mv != nil {
			if err := o.recordOpening(ctx, tx, w, *mv, cmd.CorrelationID, now); err != nil {
				return err
			}
		}
		opened = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	return opened, nil
}

func (o *WalletOpener) recordOpening(ctx context.Context, tx Tx, w *wallet.Wallet, mv wallet.Movement, correlationID string, now time.Time) error {
	txID, err := wagering.NewID(o.ids.New())
	if err != nil {
		return err
	}
	opening, err := wagering.NewOpening(txID, w.ID(), w.PlayerID(), mv.Amount, correlationID, now)
	if err != nil {
		return err
	}
	if err := opening.MarkProcessed(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, nil, now); err != nil {
		return err
	}
	if err := tx.Transactions().Insert(ctx, opening); err != nil {
		return err
	}
	entryID, err := wallet.NewEntryID(o.ids.New())
	if err != nil {
		return err
	}
	entry, err := wallet.NewLedgerEntry(entryID, w.ID(), txID.UUID(), mv, now)
	if err != nil {
		return err
	}
	if err := tx.Ledger().Insert(ctx, entry); err != nil {
		return err
	}
	events, err := outcomeEvents(o.ids, opening, &mv, "")
	if err != nil {
		return fmt.Errorf("opening events: %w", err)
	}
	return tx.Outbox().Insert(ctx, events...)
}
