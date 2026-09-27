package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type LedgerRepository struct {
	db DBTX
}

func NewLedgerRepository(db DBTX) *LedgerRepository {
	return &LedgerRepository{db: db}
}

func (r *LedgerRepository) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency,
			amount_minor, balance_before_minor, balance_after_minor, wallet_version, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID().UUID(), e.WalletID().UUID(), e.TransactionID(), string(e.Direction()),
		e.Amount().Currency().String(), e.Amount().Minor(), e.BalanceBefore().Minor(),
		e.BalanceAfter().Minor(), e.WalletVersion(), e.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("insert ledger entry: %w", err)
	}
	return nil
}

// ListByWallet returns entries in version order, starting after afterVersion.
func (r *LedgerRepository) ListByWallet(ctx context.Context, walletID wallet.ID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, wallet_id, transaction_id, direction, currency, amount_minor,
			balance_before_minor, balance_after_minor, wallet_version, created_at
		 FROM wallet_ledger_entries
		 WHERE wallet_id = $1 AND wallet_version > $2
		 ORDER BY wallet_version
		 LIMIT $3`,
		walletID.UUID(), afterVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("list ledger entries: %w", err)
	}
	defer rows.Close()

	var out []wallet.LedgerEntry
	for rows.Next() {
		var (
			id, wid, txID         uuid.UUID
			direction, currency   string
			amount, before, after int64
			version               int64
			createdAt             time.Time
		)
		if err := rows.Scan(&id, &wid, &txID, &direction, &currency, &amount, &before, &after, &version, &createdAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry: %w", err)
		}
		e, err := rehydrateEntry(id, wid, txID, direction, currency, amount, before, after, version, createdAt)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ledger entries: %w", err)
	}
	return out, nil
}

func rehydrateEntry(id, walletID, txID uuid.UUID, direction, currency string,
	amount, before, after, version int64, createdAt time.Time) (wallet.LedgerEntry, error) {
	eid, err := wallet.NewEntryID(id)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	wid, err := wallet.NewID(walletID)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	dir, err := wallet.ParseDirection(direction)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	amt, err := money.FromMinor(amount, cur)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	bef, err := money.FromMinor(before, cur)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	aft, err := money.FromMinor(after, cur)
	if err != nil {
		return wallet.LedgerEntry{}, err
	}
	e, err := wallet.RehydrateLedgerEntry(wallet.LedgerEntrySnapshot{
		ID: eid, WalletID: wid, TransactionID: txID, Direction: dir,
		Amount: amt, BalanceBefore: bef, BalanceAfter: aft, WalletVersion: version,
		CreatedAt: createdAt.UTC(),
	})
	if err != nil {
		return wallet.LedgerEntry{}, fmt.Errorf("rehydrate ledger entry %s: %w", id, err)
	}
	return e, nil
}
