package wallet

import (
	"fmt"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

func ParseDirection(s string) (Direction, error) {
	switch d := Direction(s); d {
	case DirectionDebit, DirectionCredit:
		return d, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidDirection, s)
	}
}

const InitialVersion int64 = 1

// Movement describes a balance change applied to a wallet. The wallet version
// after the movement is the version recorded on the matching ledger entry.
type Movement struct {
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	VersionBefore int64
	VersionAfter  int64
}

// Wallet is the aggregate root for a player's balance in one currency.
type Wallet struct {
	id        ID
	playerID  PlayerID
	currency  money.Currency
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// Snapshot carries persisted wallet state for rehydration.
type Snapshot struct {
	ID        ID
	PlayerID  PlayerID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Open creates a wallet at the initial version. A positive initial balance
// yields the opening credit movement, which must be recorded in the ledger.
func Open(id ID, player PlayerID, initial money.Money, now time.Time) (*Wallet, *Movement, error) {
	if id.IsZero() || player.IsZero() {
		return nil, nil, ErrInvalidID
	}
	if !initial.IsValid() || initial.IsNegative() {
		return nil, nil, fmt.Errorf("%w: initial balance must be zero or positive", ErrInvalidWallet)
	}
	if now.IsZero() {
		return nil, nil, fmt.Errorf("%w: missing timestamp", ErrInvalidWallet)
	}
	zero, err := money.Zero(initial.Currency())
	if err != nil {
		return nil, nil, err
	}
	w := &Wallet{
		id:        id,
		playerID:  player,
		currency:  initial.Currency(),
		balance:   initial,
		version:   InitialVersion,
		createdAt: now,
		updatedAt: now,
	}
	if initial.IsZero() {
		return w, nil, nil
	}
	return w, &Movement{
		Direction:     DirectionCredit,
		Amount:        initial,
		BalanceBefore: zero,
		BalanceAfter:  initial,
		VersionBefore: 0,
		VersionAfter:  InitialVersion,
	}, nil
}

// Rehydrate restores a persisted wallet without applying any movement.
func Rehydrate(s Snapshot) (*Wallet, error) {
	switch {
	case s.ID.IsZero() || s.PlayerID.IsZero():
		return nil, ErrInvalidID
	case !s.Balance.IsValid() || s.Balance.IsNegative():
		return nil, fmt.Errorf("%w: balance", ErrInvalidWallet)
	case s.Version < InitialVersion:
		return nil, fmt.Errorf("%w: version %d", ErrInvalidWallet, s.Version)
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return nil, fmt.Errorf("%w: timestamps", ErrInvalidWallet)
	}
	return &Wallet{
		id:        s.ID,
		playerID:  s.PlayerID,
		currency:  s.Balance.Currency(),
		balance:   s.Balance,
		version:   s.Version,
		createdAt: s.CreatedAt,
		updatedAt: s.UpdatedAt,
	}, nil
}

func (w *Wallet) ID() ID                   { return w.id }
func (w *Wallet) PlayerID() PlayerID       { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.currency }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }

func (w *Wallet) Snapshot() Snapshot {
	return Snapshot{
		ID:        w.id,
		PlayerID:  w.playerID,
		Balance:   w.balance,
		Version:   w.version,
		CreatedAt: w.createdAt,
		UpdatedAt: w.updatedAt,
	}
}

// Debit removes amount from the balance; the balance can never become negative.
func (w *Wallet) Debit(amount money.Money, now time.Time) (Movement, error) {
	if err := w.checkAmount(amount); err != nil {
		return Movement{}, err
	}
	after, err := w.balance.Sub(amount)
	if err != nil {
		return Movement{}, err
	}
	if after.IsNegative() {
		return Movement{}, ErrInsufficientFunds
	}
	return w.apply(DirectionDebit, amount, after, now), nil
}

func (w *Wallet) Credit(amount money.Money, now time.Time) (Movement, error) {
	if err := w.checkAmount(amount); err != nil {
		return Movement{}, err
	}
	after, err := w.balance.Add(amount)
	if err != nil {
		return Movement{}, err
	}
	return w.apply(DirectionCredit, amount, after, now), nil
}

func (w *Wallet) checkAmount(amount money.Money) error {
	if !amount.IsValid() {
		return money.ErrUninitialized
	}
	if !amount.Currency().Equal(w.currency) {
		return fmt.Errorf("%w: %s on %s wallet", ErrCurrencyMismatch, amount.Currency(), w.currency)
	}
	if !amount.IsPositive() {
		return ErrInvalidAmount
	}
	return nil
}

func (w *Wallet) apply(d Direction, amount, after money.Money, now time.Time) Movement {
	mv := Movement{
		Direction:     d,
		Amount:        amount,
		BalanceBefore: w.balance,
		BalanceAfter:  after,
		VersionBefore: w.version,
		VersionAfter:  w.version + 1,
	}
	w.balance = after
	w.version++
	if now.After(w.updatedAt) {
		w.updatedAt = now
	}
	return mv
}
