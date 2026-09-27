package wallet

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
)

// LedgerEntry is an immutable record of one balance movement.
type LedgerEntry struct {
	id            EntryID
	walletID      ID
	transactionID uuid.UUID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	walletVersion int64
	createdAt     time.Time
}

type LedgerEntrySnapshot struct {
	ID            EntryID
	WalletID      ID
	TransactionID uuid.UUID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
	CreatedAt     time.Time
}

// NewLedgerEntry records a movement produced by the wallet aggregate.
func NewLedgerEntry(id EntryID, walletID ID, transactionID uuid.UUID, mv Movement, now time.Time) (LedgerEntry, error) {
	return newLedgerEntry(LedgerEntrySnapshot{
		ID:            id,
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     mv.Direction,
		Amount:        mv.Amount,
		BalanceBefore: mv.BalanceBefore,
		BalanceAfter:  mv.BalanceAfter,
		WalletVersion: mv.VersionAfter,
		CreatedAt:     now,
	})
}

// RehydrateLedgerEntry restores a persisted entry, re-validating its arithmetic.
func RehydrateLedgerEntry(s LedgerEntrySnapshot) (LedgerEntry, error) {
	return newLedgerEntry(s)
}

func newLedgerEntry(s LedgerEntrySnapshot) (LedgerEntry, error) {
	if s.ID.IsZero() || s.WalletID.IsZero() || s.TransactionID == uuid.Nil {
		return LedgerEntry{}, ErrInvalidID
	}
	if !s.Amount.IsValid() || !s.BalanceBefore.IsValid() || !s.BalanceAfter.IsValid() {
		return LedgerEntry{}, money.ErrUninitialized
	}
	if !s.Amount.IsPositive() {
		return LedgerEntry{}, ErrInvalidAmount
	}
	if s.BalanceBefore.IsNegative() || s.BalanceAfter.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: negative balance", ErrInvalidLedgerEntry)
	}
	if s.WalletVersion < InitialVersion {
		return LedgerEntry{}, fmt.Errorf("%w: version %d", ErrInvalidLedgerEntry, s.WalletVersion)
	}
	if s.CreatedAt.IsZero() {
		return LedgerEntry{}, fmt.Errorf("%w: missing timestamp", ErrInvalidLedgerEntry)
	}

	var expected money.Money
	var err error
	switch s.Direction {
	case DirectionCredit:
		expected, err = s.BalanceBefore.Add(s.Amount)
	case DirectionDebit:
		expected, err = s.BalanceBefore.Sub(s.Amount)
	default:
		return LedgerEntry{}, ErrInvalidDirection
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if cmp, err := expected.Cmp(s.BalanceAfter); err != nil {
		return LedgerEntry{}, err
	} else if cmp != 0 {
		return LedgerEntry{}, ErrInconsistentEntry
	}

	return LedgerEntry{
		id:            s.ID,
		walletID:      s.WalletID,
		transactionID: s.TransactionID,
		direction:     s.Direction,
		amount:        s.Amount,
		balanceBefore: s.BalanceBefore,
		balanceAfter:  s.BalanceAfter,
		walletVersion: s.WalletVersion,
		createdAt:     s.CreatedAt,
	}, nil
}

func (e LedgerEntry) ID() EntryID                { return e.id }
func (e LedgerEntry) WalletID() ID               { return e.walletID }
func (e LedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) WalletVersion() int64       { return e.walletVersion }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }
