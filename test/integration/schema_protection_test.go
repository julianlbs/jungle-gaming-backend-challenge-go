//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
)

const (
	insufficientPrivilege = "42501"
	restrictViolation     = "23001"
	checkViolation        = "23514"
	uniqueViolation       = "23505"
)

func TestLedgerIsAppendOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	w := seedWallet(t, db, "100.00")
	id := w.ID.UUID()

	for _, tc := range []struct {
		name string
		pool *pgxpool.Pool
		sql  string
		want string
	}{
		{"app update", db.App, `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = $1`, insufficientPrivilege},
		{"app delete", db.App, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, insufficientPrivilege},
		{"owner update", db.Owner, `UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE wallet_id = $1`, restrictViolation},
		{"owner delete", db.Owner, `DELETE FROM wallet_ledger_entries WHERE wallet_id = $1`, restrictViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.pool.Exec(ctx, tc.sql, id)
			requireSQLState(t, err, tc.want)
		})
	}
	t.Run("owner truncate", func(t *testing.T) {
		_, err := db.Owner.Exec(ctx, `TRUNCATE wallet_ledger_entries CASCADE`)
		requireSQLState(t, err, restrictViolation)
	})
	t.Run("app truncate", func(t *testing.T) {
		_, err := db.App.Exec(ctx, `TRUNCATE wallet_ledger_entries`)
		requireSQLState(t, err, insufficientPrivilege)
	})
}

func TestLedgerChainAndWalletConsistency(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	w := seedWallet(t, db, "100.00")
	bet := newBet(t, w, "10.00")
	if err := postgres.NewTransactionRepository(db.App).Insert(ctx, bet); err != nil {
		t.Fatal(err)
	}

	insertEntry := func(before, after int64, version int64) error {
		_, err := db.App.Exec(ctx,
			`INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency,
				amount_minor, balance_before_minor, balance_after_minor, wallet_version, created_at)
			 VALUES ($1, $2, $3, 'DEBIT', 'BRL', $4, $5, $6, $7, now())`,
			uuid.New(), w.ID.UUID(), bet.ID().UUID(), before-after, before, after, version)
		return err
	}

	t.Run("entry must continue from previous balance", func(t *testing.T) {
		requireSQLState(t, insertEntry(5000, 4000, 2), checkViolation)
	})
	t.Run("entry version must advance", func(t *testing.T) {
		requireSQLState(t, insertEntry(10000, 9000, 1), checkViolation)
	})
	t.Run("arithmetic must hold", func(t *testing.T) {
		_, err := db.App.Exec(ctx,
			`INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency,
				amount_minor, balance_before_minor, balance_after_minor, wallet_version, created_at)
			 VALUES ($1, $2, $3, 'DEBIT', 'BRL', 100, 10000, 9000, 2, now())`,
			uuid.New(), w.ID.UUID(), bet.ID().UUID())
		requireSQLState(t, err, checkViolation)
	})
	t.Run("balance change without ledger entry fails at commit", func(t *testing.T) {
		tx, err := db.App.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = 1000000, version = version + 1,
			updated_at = now() WHERE id = $1`, w.ID.UUID()); err != nil {
			t.Fatalf("update should be deferred to commit: %v", err)
		}
		requireSQLState(t, tx.Commit(ctx), checkViolation)
	})
	t.Run("balance change must bump version by one", func(t *testing.T) {
		_, err := db.App.Exec(ctx, `UPDATE wallets SET balance_minor = 1, version = version + 2 WHERE id = $1`, w.ID.UUID())
		requireSQLState(t, err, checkViolation)
	})
	t.Run("balance cannot go negative", func(t *testing.T) {
		_, err := db.App.Exec(ctx, `UPDATE wallets SET balance_minor = -1, version = version + 1 WHERE id = $1`, w.ID.UUID())
		requireSQLState(t, err, checkViolation)
	})
	t.Run("matching ledger entry commits", func(t *testing.T) {
		err := db.UoW.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = 9000, version = 2, updated_at = now()
				WHERE id = $1`, w.ID.UUID()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO wallet_ledger_entries (id, wallet_id, transaction_id, direction, currency,
					amount_minor, balance_before_minor, balance_after_minor, wallet_version, created_at)
				 VALUES ($1, $2, $3, 'DEBIT', 'BRL', 1000, 10000, 9000, 2, now())`,
				uuid.New(), w.ID.UUID(), bet.ID().UUID())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRecordsCannotBeRemovedOrRewritten(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	w := seedWallet(t, db, "100.00")

	for _, tc := range []struct {
		name string
		pool *pgxpool.Pool
		sql  string
		arg  any
		want string
	}{
		{"app cannot delete wallets", db.App, `DELETE FROM wallets WHERE id = $1`, w.ID.UUID(), insufficientPrivilege},
		{"owner cannot delete wallets", db.Owner, `DELETE FROM wallets WHERE id = $1`, w.ID.UUID(), restrictViolation},
		{"wallet identity is immutable", db.App, `UPDATE wallets SET player_id = gen_random_uuid() WHERE id = $1`, w.ID.UUID(), restrictViolation},
		{"app cannot delete transactions", db.App, `DELETE FROM wager_transactions WHERE id = $1`, w.OpeningID.UUID(), insufficientPrivilege},
		{"owner cannot delete transactions", db.Owner, `DELETE FROM wager_transactions WHERE id = $1`, w.OpeningID.UUID(), restrictViolation},
		{"terminal transaction is immutable", db.App, `UPDATE wager_transactions SET failure_detail = 'x' WHERE id = $1`, w.OpeningID.UUID(), restrictViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.pool.Exec(ctx, tc.sql, tc.arg)
			requireSQLState(t, err, tc.want)
		})
	}

	t.Run("pending transaction business fields are immutable", func(t *testing.T) {
		bet := newBet(t, w, "1.00")
		if err := postgres.NewTransactionRepository(db.App).Insert(ctx, bet); err != nil {
			t.Fatal(err)
		}
		_, err := db.App.Exec(ctx, `UPDATE wager_transactions SET amount_minor = 99999 WHERE id = $1`, bet.ID().UUID())
		requireSQLState(t, err, restrictViolation)
	})
	t.Run("a second opening is refused", func(t *testing.T) {
		_, err := db.App.Exec(ctx,
			`INSERT INTO wager_transactions (id, origin, kind, status, channel, wallet_id, player_id, currency,
				amount_minor, result_balance_minor, result_wallet_version, correlation_id,
				created_at, updated_at, completed_at)
			 VALUES (gen_random_uuid(), 'INTERNAL', 'OPENING', 'PROCESSED', 'INTERNAL', $1, $2, 'BRL',
				100, 100, 1, 'c', now(), now(), now())`,
			w.ID.UUID(), w.PlayerID.UUID())
		requireSQLState(t, err, uniqueViolation)
	})
}

func TestApplicationRoleCannotChangeSchema(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, sql := range []string{
		`CREATE TABLE sneaky (id int)`,
		`ALTER TABLE wallet_ledger_entries DISABLE TRIGGER ALL`,
		`DROP TRIGGER ledger_no_update_delete ON wallet_ledger_entries`,
		`ALTER TABLE wallets DROP CONSTRAINT wallets_balance_minor_check`,
		`UPDATE inbox_messages SET outcome = 'PROCESSED'`,
	} {
		t.Run(sql, func(t *testing.T) {
			_, err := db.App.Exec(ctx, sql)
			requireSQLState(t, err, insufficientPrivilege)
		})
	}
}
