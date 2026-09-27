package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/event"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

// UnitOfWork runs fn in a single database transaction. fn may be called more than once when a
// transient failure is retried, so it must not produce effects outside the transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx exposes the repositories bound to one open transaction.
type Tx interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

type WalletRepository interface {
	Insert(ctx context.Context, w *wallet.Wallet) error
	// GetForUpdate locks the wallet until the transaction ends.
	GetForUpdate(ctx context.Context, id wallet.ID) (*wallet.Wallet, error)
	UpdateBalance(ctx context.Context, w *wallet.Wallet, expectedVersion int64) error
}

type TransactionRepository interface {
	Insert(ctx context.Context, t *wagering.Transaction) error
	Update(ctx context.Context, t *wagering.Transaction) error
	GetForUpdate(ctx context.Context, id wagering.ID) (*wagering.Transaction, error)
	FindByIdempotencyKey(ctx context.Context, provider, key string) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, provider, externalID string) (*wagering.Transaction, error)
	HasProcessedReversal(ctx context.Context, referenceID wagering.ID) (bool, error)
}

type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
}

type OutboxRepository interface {
	Insert(ctx context.Context, events ...event.Outgoing) error
}

type InboxRepository interface {
	Find(ctx context.Context, consumer, messageID string) (InboxEntry, error)
	Insert(ctx context.Context, e InboxEntry) error
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	New() uuid.UUID
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// TimeOrderedIDs issues UUIDv7 values, which keep primary key indexes append-friendly.
type TimeOrderedIDs struct{}

func (TimeOrderedIDs) New() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
