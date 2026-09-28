package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type ReadModel struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

func NewReadModel(pool *pgxpool.Pool) *ReadModel {
	return &ReadModel{pool: pool, queryTimeout: DefaultTxConfig().StatementTimeout}
}

// WithQueryTimeout bounds every read, including reconciliation, by a context deadline and
// a transaction-local statement_timeout. A non-positive duration makes the deadline already past.
func (r *ReadModel) WithQueryTimeout(d time.Duration) *ReadModel {
	if d != 0 {
		r.queryTimeout = d
	}
	return r
}

func (r *ReadModel) read(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin read: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`, pgInterval(r.queryTimeout)); err != nil {
		return fmt.Errorf("set statement timeout: %w", err)
	}
	return fn(ctx, tx)
}

func (r *ReadModel) GetWallet(ctx context.Context, id wallet.ID) (*wallet.Wallet, error) {
	var w *wallet.Wallet
	err := r.read(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		w, err = NewWalletRepository(tx).Get(ctx, id)
		return err
	})
	return w, err
}

func (r *ReadModel) ListLedger(ctx context.Context, id wallet.ID, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	var entries []wallet.LedgerEntry
	err := r.read(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		entries, err = NewLedgerRepository(tx).ListByWallet(ctx, id, afterVersion, limit)
		return err
	})
	return entries, err
}

func (r *ReadModel) GetTransaction(ctx context.Context, id wagering.ID) (*wagering.Transaction, error) {
	var txn *wagering.Transaction
	err := r.read(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		txn, err = NewTransactionRepository(tx).Get(ctx, id)
		return err
	})
	return txn, err
}

func (r *ReadModel) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.Transaction, error) {
	var txn *wagering.Transaction
	err := r.read(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		txn, err = NewTransactionRepository(tx).FindByExternalID(ctx, providerID, externalID)
		return err
	})
	return txn, err
}

// LedgerTotals reads the wallet and aggregates its ledger from a single snapshot.
func (r *ReadModel) LedgerTotals(ctx context.Context, id wallet.ID) (app.LedgerTotals, error) {
	var totals app.LedgerTotals
	err := r.read(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		w, err := NewWalletRepository(tx).Get(ctx, id)
		if err != nil {
			return err
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
			return fmt.Errorf("aggregate ledger: %w", err)
		}
		if !inRange || sum == nil {
			return app.ErrReconciliationOverflow
		}
		totals = app.LedgerTotals{
			Wallet:             w,
			LedgerSumMinor:     *sum,
			EntryCount:         count,
			LatestEntryVersion: latestVer,
			LatestBalanceMinor: latestBal,
		}
		return nil
	})
	return totals, err
}

var _ app.ReadModel = (*ReadModel)(nil)
