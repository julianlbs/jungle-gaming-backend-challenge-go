package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	URL               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	HealthCheckPeriod time.Duration
	// StatementTimeout is applied to every new session. Zero leaves the server default.
	StatementTimeout time.Duration
	Tracer           pgx.QueryTracer
}

func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.HealthCheckPeriod > 0 {
		pc.HealthCheckPeriod = cfg.HealthCheckPeriod
	}
	if cfg.Tracer != nil {
		pc.ConnConfig.Tracer = cfg.Tracer
	}
	if cfg.StatementTimeout > 0 {
		timeout := pgInterval(cfg.StatementTimeout)
		pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SELECT set_config('statement_timeout', $1, false)", timeout)
			if err != nil {
				return fmt.Errorf("set statement timeout: %w", err)
			}
			return nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return pool, nil
}

// PingWithRetry waits until the database answers or ctx expires.
func PingWithRetry(ctx context.Context, pool *pgxpool.Pool) error {
	delay := 100 * time.Millisecond
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			if errors.Is(err, ctx.Err()) {
				return fmt.Errorf("postgres not reachable: %w", err)
			}
			return fmt.Errorf("postgres not reachable: %w", errors.Join(err, ctx.Err()))
		}
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(delay*2, 2*time.Second)
	}
}
