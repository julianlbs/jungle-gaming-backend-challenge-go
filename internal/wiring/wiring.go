// Package wiring is the composition root: it assembles adapters and use cases with Fx.
package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/config"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

// Options returns the full application for an already validated configuration.
func Options(cfg config.Config) fx.Option {
	// Fx stops hooks in reverse registration order. Platform goes first so that metrics stay
	// scrapeable until the end; the API goes last so that on shutdown it turns unready and
	// drains before the workers stop.
	var roles []fx.Option
	if cfg.Roles.Has(config.RoleConsumer) || cfg.Roles.Has(config.RoleOutbox) {
		roles = append(roles, AWS)
	}
	if cfg.Roles.Has(config.RoleConsumer) {
		roles = append(roles, Consumer)
	}
	if cfg.Roles.Has(config.RoleOutbox) {
		roles = append(roles, Outbox)
	}
	if cfg.Roles.Has(config.RolePending) {
		roles = append(roles, Pending)
	}
	if cfg.Roles.Has(config.RoleAPI) {
		roles = append(roles, API)
	}
	return fx.Options(
		Platform,
		fx.Options(roles...),
		fx.Supply(cfg),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: log}
			l.UseLogLevel(slog.LevelDebug)
			return l
		}),
		Postgres,
		App,
	)
}

var Platform = fx.Module("platform",
	fx.Provide(
		func(cfg config.Config) *slog.Logger {
			return logging.New(os.Stdout, cfg.LogLevel, cfg.InstanceID)
		},
		metrics.New,
	),
	fx.Invoke(registerMetricsServer),
)

var Postgres = fx.Module("postgres",
	fx.Provide(
		newPool,
		newUnitOfWork,
		func(u *postgres.UnitOfWork) app.UnitOfWork { return u },
		postgres.NewReadModel,
		func(pool *pgxpool.Pool) *postgres.PendingStore { return postgres.NewPendingStore(pool) },
		func(pool *pgxpool.Pool) *postgres.OutboxStore { return postgres.NewOutboxStore(pool) },
	),
	// Every role needs the database, so its reachability is checked at start.
	fx.Invoke(func(*pgxpool.Pool) {}),
)

var App = fx.Module("app",
	fx.Provide(
		func() app.Clock { return app.SystemClock{} },
		func() app.IDGenerator { return app.TimeOrderedIDs{} },
		pendingPolicy,
		app.NewWalletOpener,
		func(uow app.UnitOfWork, clock app.Clock, ids app.IDGenerator, policy app.PendingPolicy, store *postgres.PendingStore, m *metrics.Metrics) *app.WagerProcessor {
			return app.NewWagerProcessor(uow, clock, ids, policy, store).WithObserver(wagerObserver(m))
		},
		func(uow app.UnitOfWork, clock app.Clock, ids app.IDGenerator, policy app.PendingPolicy, store *postgres.PendingStore) *app.PendingResumer {
			return app.NewPendingResumer(uow, faultyPendingQueue{store}, clock, ids, policy, store)
		},
		func(read *postgres.ReadModel, clock app.Clock) *app.Queries { return app.NewQueries(read, clock) },
	),
)

func newPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := postgres.NewPool(context.Background(), postgres.PoolConfig{
		URL:      cfg.Postgres.URL,
		MaxConns: cfg.Postgres.MaxConns,
	})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, cfg.Postgres.StartupTimeout)
			defer cancel()
			if err := postgres.PingWithRetry(ctx, pool); err != nil {
				return err
			}
			log.Info("postgres connected", "maxConns", cfg.Postgres.MaxConns)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

func newUnitOfWork(pool *pgxpool.Pool, cfg config.Config, m *metrics.Metrics) *postgres.UnitOfWork {
	retry := postgres.DefaultRetryPolicy()
	retry.OnRetry = m.ObserveDBRetry
	retry.OnFailure = m.ObserveDBFailure
	return postgres.NewUnitOfWork(pool, postgres.TxConfig{
		LockTimeout:      cfg.Postgres.LockTimeout,
		StatementTimeout: cfg.Postgres.StatementTimeout,
	}, retry)
}

func wagerObserver(m *metrics.Metrics) app.WagerObserver {
	return func(o app.WagerObservation) {
		m.ObserveWager(o.Channel, o.Kind, o.Status, o.Replay, o.Conflict, o.ConcurrencyConflict, o.Duration)
	}
}

func pendingPolicy(cfg config.Config) app.PendingPolicy {
	return app.PendingPolicy{
		BaseDelay:   cfg.Pending.BackoffBase,
		MaxDelay:    cfg.Pending.BackoffMax,
		MaxAttempts: cfg.Pending.MaxAttempts,
		TTL:         cfg.Pending.TTL,
		Jitter:      0.2,
	}
}

// registerMetricsServer binds the metrics port during start so that a port conflict fails
// the start instead of surfacing later.
func registerMetricsServer(lc fx.Lifecycle, cfg config.Config, m *metrics.Metrics, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := new(net.ListenConfig).Listen(ctx, "tcp", cfg.MetricsAddr)
			if err != nil {
				return fmt.Errorf("metrics listener: %w", err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("metrics server stopped", "error", err)
				}
			}()
			log.Info("metrics server listening", "addr", ln.Addr().String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return srv.Shutdown(ctx)
		},
	})
}
