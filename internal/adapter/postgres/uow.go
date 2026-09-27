package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TxConfig struct {
	LockTimeout      time.Duration
	StatementTimeout time.Duration
}

func DefaultTxConfig() TxConfig {
	return TxConfig{LockTimeout: 2 * time.Second, StatementTimeout: 5 * time.Second}
}

// UnitOfWork runs a callback inside one READ COMMITTED transaction, retrying the whole
// callback on transient failures. The callback must therefore be free of external side effects.
type UnitOfWork struct {
	pool  *pgxpool.Pool
	cfg   TxConfig
	retry RetryPolicy
}

func NewUnitOfWork(pool *pgxpool.Pool, cfg TxConfig, retry RetryPolicy) *UnitOfWork {
	return &UnitOfWork{pool: pool, cfg: cfg, retry: retry}
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return u.retry.do(ctx, func(ctx context.Context) error {
		return u.attempt(ctx, fn)
	})
}

func (u *UnitOfWork) attempt(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx,
		`SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
		pgInterval(u.cfg.LockTimeout), pgInterval(u.cfg.StatementTimeout),
	); err != nil {
		return fmt.Errorf("set timeouts: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func pgInterval(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
