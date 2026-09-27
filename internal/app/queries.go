package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

const (
	DefaultLedgerPageSize = 50
	MaxLedgerPageSize     = 200
)

// ErrReconciliationOverflow means the ledger sum does not fit in the money range.
var ErrReconciliationOverflow = errors.New("ledger sum exceeds the supported range")

// LedgerTotals is a consistent snapshot of a wallet and its ledger.
type LedgerTotals struct {
	Wallet             *wallet.Wallet
	LedgerSumMinor     int64
	EntryCount         int64
	LatestEntryVersion int64
	LatestBalanceMinor int64
}

// ReadModel serves queries outside of write transactions.
type ReadModel interface {
	GetWallet(ctx context.Context, id wallet.ID) (*wallet.Wallet, error)
	ListLedger(ctx context.Context, id wallet.ID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
	GetTransaction(ctx context.Context, id wagering.ID) (*wagering.Transaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error)
	LedgerTotals(ctx context.Context, id wallet.ID) (LedgerTotals, error)
}

// Viewer is the caller of a transaction query.
type Viewer struct {
	ProviderID string
	ReadAll    bool
}

type LedgerPage struct {
	Items      []wallet.LedgerEntry
	NextCursor string
}

type Reconciliation struct {
	Wallet     *wallet.Wallet
	Stored     money.Money
	Calculated money.Money
	Difference money.Money
	EntryCount int64
	Consistent bool
	CheckedAt  time.Time
}

type Queries struct {
	read  ReadModel
	clock Clock
}

func NewQueries(read ReadModel, clock Clock) *Queries {
	return &Queries{read: read, clock: clock}
}

func (q *Queries) Wallet(ctx context.Context, walletID string) (*wallet.Wallet, error) {
	id, err := parseWalletID(walletID)
	if err != nil {
		return nil, err
	}
	return q.read.GetWallet(ctx, id)
}

func (q *Queries) Ledger(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error) {
	id, err := parseWalletID(walletID)
	if err != nil {
		return LedgerPage{}, err
	}
	if limit == 0 {
		limit = DefaultLedgerPageSize
	}
	if limit < 1 || limit > MaxLedgerPageSize {
		return LedgerPage{}, &wagering.FieldError{Field: "limit", Reason: fmt.Sprintf("must be between 1 and %d", MaxLedgerPageSize)}
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		return LedgerPage{}, err
	}
	if _, err := q.read.GetWallet(ctx, id); err != nil {
		return LedgerPage{}, err
	}
	items, err := q.read.ListLedger(ctx, id, after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(page.Items[limit-1].WalletVersion())
	}
	return page, nil
}

// Transaction returns a transaction visible to the viewer; others' transactions are reported
// as not found so that their existence is not revealed.
func (q *Queries) Transaction(ctx context.Context, transactionID string, viewer Viewer) (*wagering.Transaction, error) {
	id, err := wagering.ParseID(transactionID)
	if err != nil {
		return nil, ErrNotFound
	}
	t, err := q.read.GetTransaction(ctx, id)
	if err != nil {
		return nil, err
	}
	if viewer.ReadAll {
		return t, nil
	}
	if ext, ok := t.External(); !ok || viewer.ProviderID == "" || ext.ProviderID != viewer.ProviderID {
		return nil, ErrNotFound
	}
	return t, nil
}

func (q *Queries) TransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	if providerID == "" || externalID == "" {
		return nil, ErrNotFound
	}
	return q.read.FindByExternalID(ctx, providerID, externalID)
}

// Reconcile compares the stored balance with the sum of the ledger. It never writes.
func (q *Queries) Reconcile(ctx context.Context, walletID string) (Reconciliation, error) {
	id, err := parseWalletID(walletID)
	if err != nil {
		return Reconciliation{}, err
	}
	totals, err := q.read.LedgerTotals(ctx, id)
	if err != nil {
		return Reconciliation{}, err
	}
	w := totals.Wallet
	calculated, err := money.FromMinor(totals.LedgerSumMinor, w.Currency())
	if err != nil {
		return Reconciliation{}, err
	}
	diff, err := w.Balance().Sub(calculated)
	if err != nil {
		return Reconciliation{}, err
	}
	chainMatches := totals.EntryCount == 0 && w.Version() == wallet.InitialVersion && w.Balance().IsZero() ||
		totals.LatestEntryVersion == w.Version() && totals.LatestBalanceMinor == w.Balance().Minor()
	return Reconciliation{
		Wallet:     w,
		Stored:     w.Balance(),
		Calculated: calculated,
		Difference: diff,
		EntryCount: totals.EntryCount,
		Consistent: diff.IsZero() && chainMatches,
		CheckedAt:  q.clock.Now(),
	}, nil
}

func parseWalletID(s string) (wallet.ID, error) {
	id, err := wallet.ParseID(s)
	if err != nil {
		return wallet.ID{}, ErrNotFound
	}
	return id, nil
}

type cursorPayload struct {
	V int64 `json:"v"`
}

func encodeCursor(version int64) string {
	b, _ := json.Marshal(cursorPayload{V: version})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	invalid := &wagering.FieldError{Field: "cursor", Reason: "is invalid"}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, invalid
	}
	var c cursorPayload
	if err := json.Unmarshal(raw, &c); err != nil || c.V < 0 {
		return 0, invalid
	}
	return c.V, nil
}
