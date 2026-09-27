package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// WagerCommand is the channel-independent form of a provider operation. HTTP and SQS build it
// from their raw inputs so that validation and the payload hash are identical by construction.
type WagerCommand struct {
	Channel                        wagering.Channel
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	WalletID                       string
	PlayerID                       string
	Kind                           string
	Amount                         string
	Currency                       string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
	CorrelationID                  string
	CausationID                    string
}

// WagerOutcome is the persisted transaction; Replay marks a response served from an earlier request.
type WagerOutcome struct {
	Transaction *wagering.Transaction
	Replay      bool
}

// ReferenceNudger brings forward operations waiting for a reference that has just been processed.
type ReferenceNudger interface {
	NudgeWaiting(ctx context.Context, providerID, referenceExternalID string) error
}

type WagerProcessor struct {
	uow      UnitOfWork
	clock    Clock
	ids      IDGenerator
	policy   PendingPolicy
	nudger   ReferenceNudger
	observer WagerObserver
}

func NewWagerProcessor(uow UnitOfWork, clock Clock, ids IDGenerator, policy PendingPolicy, nudger ReferenceNudger) *WagerProcessor {
	return &WagerProcessor{uow: uow, clock: clock, ids: ids, policy: policy, nudger: nudger}
}

// WithObserver reports the result of every Process call and of every message handled by a
// WagerIntake built on this processor.
func (p *WagerProcessor) WithObserver(o WagerObserver) *WagerProcessor {
	p.observer = o
	return p
}

// Process handles one operation in its own transaction.
func (p *WagerProcessor) Process(ctx context.Context, cmd WagerCommand) (WagerOutcome, error) {
	start := time.Now()
	ctx, span := tracer.Start(ctx, "wager.process")
	defer span.End()
	var out WagerOutcome
	err := p.uow.Do(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.Execute(ctx, tx, cmd)
		return err
	})
	if err != nil {
		p.observe(ctx, cmd, WagerOutcome{}, false, err, start)
		return WagerOutcome{}, err
	}
	p.observe(ctx, cmd, out, false, nil, start)
	p.nudge(ctx, out)
	return out, nil
}

// Execute runs inside a caller-provided transaction so that a consumer can record its inbox
// entry atomically with the financial effects. Callers should call AfterCommit once committed.
func (p *WagerProcessor) Execute(ctx context.Context, tx Tx, cmd WagerCommand) (WagerOutcome, error) {
	now := p.clock.Now()
	t, err := p.build(cmd, now)
	if err != nil {
		return WagerOutcome{}, err
	}

	if out, ok, err := p.replay(ctx, tx, t); err != nil || ok {
		return out, err
	}

	w, err := tx.Wallets().GetForUpdate(ctx, t.WalletID())
	if errors.Is(err, ErrNotFound) {
		return WagerOutcome{}, ErrWalletNotFound
	}
	if err != nil {
		return WagerOutcome{}, err
	}
	if w.PlayerID() != t.PlayerID() || !w.Currency().Equal(t.Amount().Currency()) {
		return WagerOutcome{}, ErrWalletMismatch
	}

	// A concurrent request for the same operation may have committed while we waited for the lock.
	if out, ok, err := p.replay(ctx, tx, t); err != nil || ok {
		return out, err
	}

	ref, reversed, err := loadReference(ctx, tx, t)
	if err != nil {
		return WagerOutcome{}, err
	}
	eval := wagering.EvaluateReference(t, ref, reversed)

	var mv *wallet.Movement
	if eval.Decision == wagering.DecisionWait {
		if err := t.AwaitReference(now.Add(p.policy.Delay(0)), now.Add(p.policy.TTL), now); err != nil {
			return WagerOutcome{}, err
		}
	} else if mv, err = settle(t, w, ref, eval, now); err != nil {
		return WagerOutcome{}, err
	}

	if err := tx.Transactions().Insert(ctx, t); err != nil {
		return WagerOutcome{}, err
	}
	if err := persistEffects(ctx, tx, p.ids, t, w, mv, cmd.CausationID); err != nil {
		return WagerOutcome{}, err
	}
	return WagerOutcome{Transaction: t}, nil
}

// AfterCommit performs best-effort work that must not run inside the transaction.
func (p *WagerProcessor) AfterCommit(ctx context.Context, out WagerOutcome) {
	p.nudge(ctx, out)
}

func (p *WagerProcessor) nudge(ctx context.Context, out WagerOutcome) {
	if p.nudger == nil || out.Replay || out.Transaction == nil || out.Transaction.Status() != wagering.StatusProcessed {
		return
	}
	if ext, ok := out.Transaction.External(); ok {
		_ = p.nudger.NudgeWaiting(context.WithoutCancel(ctx), ext.ProviderID, ext.ExternalID)
	}
}

func (p *WagerProcessor) build(cmd WagerCommand, now time.Time) (*wagering.Transaction, error) {
	kind, err := wagering.ParseExternalKind(cmd.Kind)
	if errors.Is(err, wagering.ErrReservedKind) {
		return nil, err
	}
	if err != nil {
		return nil, &wagering.FieldError{Field: "kind", Reason: "unknown kind"}
	}
	walletID, err := wallet.ParseID(cmd.WalletID)
	if err != nil {
		return nil, &wagering.FieldError{Field: "walletId", Reason: "must be a UUID"}
	}
	playerID, err := wallet.ParsePlayerID(cmd.PlayerID)
	if err != nil {
		return nil, &wagering.FieldError{Field: "playerId", Reason: "must be a UUID"}
	}
	currency, err := money.ParseCurrency(cmd.Currency)
	if err != nil {
		return nil, &wagering.FieldError{Field: "money.currency", Reason: "unsupported currency"}
	}
	amount, err := money.Parse(cmd.Amount, currency)
	if err != nil {
		return nil, &wagering.FieldError{Field: "money.amount", Reason: "must be a non-negative decimal with up to 2 places"}
	}
	id, err := wagering.NewID(p.ids.New())
	if err != nil {
		return nil, err
	}
	return wagering.NewExternal(wagering.ExternalParams{
		ID:                  id,
		Channel:             cmd.Channel,
		WalletID:            walletID,
		PlayerID:            playerID,
		Kind:                kind,
		Amount:              amount,
		ProviderID:          cmd.ProviderID,
		ExternalID:          cmd.ExternalTransactionID,
		IdempotencyKey:      cmd.IdempotencyKey,
		RoundID:             cmd.RoundID,
		GameID:              cmd.GameID,
		ReferenceExternalID: cmd.ReferenceExternalTransactionID,
		CorrelationID:       cmd.CorrelationID,
		Now:                 now,
	})
}

func (p *WagerProcessor) replay(ctx context.Context, tx Tx, t *wagering.Transaction) (WagerOutcome, bool, error) {
	ext, _ := t.External()
	existing, err := tx.Transactions().FindByIdempotencyKey(ctx, ext.ProviderID, ext.IdempotencyKey)
	switch {
	case err == nil:
		return replayOf(existing, ext)
	case !errors.Is(err, ErrNotFound):
		return WagerOutcome{}, false, err
	}
	// Each statement sees its own snapshot, so a concurrent request for the same operation may
	// commit between the two lookups; only a different key is a conflict.
	existing, err = tx.Transactions().FindByExternalID(ctx, ext.ProviderID, ext.ExternalID)
	switch {
	case err == nil:
		if prev, ok := existing.External(); ok && prev.IdempotencyKey == ext.IdempotencyKey {
			return replayOf(existing, ext)
		}
		return WagerOutcome{}, false, ErrExternalTransactionConflict
	case !errors.Is(err, ErrNotFound):
		return WagerOutcome{}, false, err
	}
	return WagerOutcome{}, false, nil
}

func replayOf(existing *wagering.Transaction, ext wagering.External) (WagerOutcome, bool, error) {
	if !existing.SamePayload(ext.PayloadHash) {
		return WagerOutcome{}, false, ErrIdempotencyKeyReused
	}
	return WagerOutcome{Transaction: existing, Replay: true}, true, nil
}

// loadReference returns the referenced transaction (nil when absent) and whether it was reversed.
func loadReference(ctx context.Context, tx Tx, t *wagering.Transaction) (*wagering.Transaction, bool, error) {
	ext, _ := t.External()
	if ext.ReferenceExternalID == "" {
		return nil, false, nil
	}
	ref, err := tx.Transactions().FindByExternalID(ctx, ext.ProviderID, ext.ReferenceExternalID)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	reversed, err := tx.Transactions().HasProcessedReversal(ctx, ref.ID())
	if err != nil {
		return nil, false, err
	}
	return ref, reversed, nil
}

// settle applies an Apply or Reject decision to the transaction and the locked wallet and returns
// the balance movement, if any.
func settle(t *wagering.Transaction, w *wallet.Wallet, ref *wagering.Transaction, eval wagering.Evaluation, now time.Time) (*wallet.Movement, error) {
	var refID *wagering.ID
	if ref != nil {
		id := ref.ID()
		refID = &id
	}
	current := func() wagering.Result {
		return wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}
	}

	switch eval.Decision {
	case wagering.DecisionReject:
		return nil, t.Reject(eval.Code, current(), refID, now)
	case wagering.DecisionApply:
	default:
		return nil, fmt.Errorf("cannot settle decision %s", eval.Decision)
	}

	var (
		mv  wallet.Movement
		err error
	)
	switch wagering.EffectOf(t, ref) {
	case wagering.EffectNone:
		return nil, t.MarkProcessed(current(), refID, now)
	case wagering.EffectDebit:
		mv, err = w.Debit(t.Amount(), now)
		if errors.Is(err, wallet.ErrInsufficientFunds) {
			return nil, t.Reject(wagering.InsufficientFundsCode(t.Kind()), current(), refID, now)
		}
	case wagering.EffectCredit:
		mv, err = w.Credit(t.Amount(), now)
	}
	if err != nil {
		return nil, err
	}
	if err := t.MarkProcessed(current(), refID, now); err != nil {
		return nil, err
	}
	return &mv, nil
}

// persistEffects writes the wallet, ledger and events of an already stored transaction state.
func persistEffects(ctx context.Context, tx Tx, ids IDGenerator, t *wagering.Transaction, w *wallet.Wallet, mv *wallet.Movement, causationID string) error {
	if mv != nil {
		if err := tx.Wallets().UpdateBalance(ctx, w, mv.VersionBefore); err != nil {
			return err
		}
		entryID, err := wallet.NewEntryID(ids.New())
		if err != nil {
			return err
		}
		entry, err := wallet.NewLedgerEntry(entryID, w.ID(), t.ID().UUID(), *mv, t.UpdatedAt())
		if err != nil {
			return err
		}
		if err := tx.Ledger().Insert(ctx, entry); err != nil {
			return err
		}
	}
	events, err := outcomeEvents(ids, t, mv, causationID)
	if err != nil {
		return fmt.Errorf("wager events: %w", err)
	}
	return tx.Outbox().Insert(ctx, events...)
}
