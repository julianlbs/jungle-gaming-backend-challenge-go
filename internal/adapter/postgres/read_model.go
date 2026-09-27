package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type ReadModel struct {
	pool *pgxpool.Pool
}

func NewReadModel(pool *pgxpool.Pool) *ReadModel {
	return &ReadModel{pool: pool}
}

func (r *ReadModel) GetWallet(ctx context.Context, id wallet.ID) (*wallet.Wallet, error) {
	return NewWalletRepository(r.pool).Get(ctx, id)
}

func (r *ReadModel) ListLedger(ctx context.Context, id wallet.ID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	return NewLedgerRepository(r.pool).ListByWallet(ctx, id, afterVersion, limit)
}

func (r *ReadModel) GetTransaction(ctx context.Context, id wagering.ID) (*wagering.Transaction, error) {
	return NewTransactionRepository(r.pool).Get(ctx, id)
}

func (r *ReadModel) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	return NewTransactionRepository(r.pool).FindByExternalID(ctx, providerID, externalID)
}

// LedgerTotals reads the wallet and aggregates its ledger from a single snapshot.
func (r *ReadModel) LedgerTotals(ctx context.Context, id wallet.ID) (app.LedgerTotals, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return app.LedgerTotals{}, fmt.Errorf("begin reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	w, err := NewWalletRepository(tx).Get(ctx, id)
	if err != nil {
		return app.LedgerTotals{}, err
	}

	var (
		sum       *int64
		inRange   bool
		count     int64
		latestVer int64
		latestBal int64
	)
	err = tx.QueryRow(ctx,
		`WITH totals AS (
			SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor::numeric
			                                   ELSE -amount_minor::numeric END), 0) AS total,
			       count(*) AS entries
			  FROM wallet_ledger_entries WHERE wallet_id = $1),
		 latest AS (
			SELECT wallet_version, balance_after_minor
			  FROM wallet_ledger_entries WHERE wallet_id = $1
			 ORDER BY wallet_version DESC LIMIT 1)
		 SELECT CASE WHEN t.total BETWEEN -9223372036854775808 AND 9223372036854775807 THEN t.total::bigint END,
		        t.total BETWEEN -9223372036854775808 AND 9223372036854775807,
		        t.entries,
		        COALESCE(l.wallet_version, 0),
		        COALESCE(l.balance_after_minor, 0)
		   FROM totals t LEFT JOIN latest l ON true`,
		id.UUID()).Scan(&sum, &inRange, &count, &latestVer, &latestBal)
	if err != nil {
		return app.LedgerTotals{}, fmt.Errorf("aggregate ledger: %w", err)
	}
	if !inRange || sum == nil {
		return app.LedgerTotals{}, app.ErrReconciliationOverflow
	}
	return app.LedgerTotals{
		Wallet:             w,
		LedgerSumMinor:     *sum,
		EntryCount:         count,
		LatestEntryVersion: latestVer,
		LatestBalanceMinor: latestBal,
	}, nil
}

var _ app.ReadModel = (*ReadModel)(nil)
