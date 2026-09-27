//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

const (
	defaultOwnerURL = "postgres://wallet_owner:wallet_owner@localhost:5432/wallet?sslmode=disable"
	defaultAppURL   = "postgres://wallet_app:wallet_app@localhost:5432/wallet?sslmode=disable"
)

var env struct {
	ownerURL string
	appURL   string
	template string
}

func TestMain(m *testing.M) {
	env.ownerURL = getenv("TEST_DATABASE_OWNER_URL", defaultOwnerURL)
	env.appURL = getenv("TEST_DATABASE_APP_URL", defaultAppURL)
	env.template = fmt.Sprintf("wallet_tpl_%d", os.Getpid())

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := createTemplate(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		os.Exit(1)
	}
	code := m.Run()

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	if err := dropDatabase(ctx, env.template); err != nil {
		fmt.Fprintln(os.Stderr, "integration teardown:", err)
	}
	cancel()
	os.Exit(code)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func withDatabase(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

func adminExec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, env.ownerURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func createEmptyDatabase(ctx context.Context, name string) error {
	if err := adminExec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE template0`, pgx.Identifier{name}.Sanitize())); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	conn, err := pgx.Connect(ctx, withDatabase(env.ownerURL, name))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `REVOKE ALL ON SCHEMA public FROM PUBLIC;
		ALTER SCHEMA public OWNER TO wallet_owner;
		GRANT USAGE ON SCHEMA public TO wallet_app`)
	return err
}

func createTemplate(ctx context.Context) error {
	_ = dropDatabase(ctx, env.template)
	if err := createEmptyDatabase(ctx, env.template); err != nil {
		return err
	}
	m, err := postgres.NewMigrator(withDatabase(env.ownerURL, env.template))
	if err != nil {
		return err
	}
	defer m.Close()
	return m.Up()
}

func dropDatabase(ctx context.Context, name string) error {
	return adminExec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{name}.Sanitize()))
}

// testDB is an isolated, fully migrated database dropped when the test ends.
type testDB struct {
	Name  string
	App   *pgxpool.Pool
	Owner *pgxpool.Pool
	UoW   *postgres.UnitOfWork
}

func newTestDB(t *testing.T) *testDB {
	t.Helper()
	ctx := context.Background()
	name := "wallet_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if err := adminExec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`,
		pgx.Identifier{name}.Sanitize(), pgx.Identifier{env.template}.Sanitize())); err != nil {
		t.Fatalf("clone template: %v", err)
	}
	db := &testDB{Name: name}
	t.Cleanup(func() {
		if db.App != nil {
			db.App.Close()
		}
		if db.Owner != nil {
			db.Owner.Close()
		}
		if err := dropDatabase(context.Background(), name); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})

	var err error
	if db.App, err = postgres.NewPool(ctx, postgres.PoolConfig{URL: withDatabase(env.appURL, name), MaxConns: 16}); err != nil {
		t.Fatal(err)
	}
	if db.Owner, err = postgres.NewPool(ctx, postgres.PoolConfig{URL: withDatabase(env.ownerURL, name), MaxConns: 4}); err != nil {
		t.Fatal(err)
	}
	db.UoW = postgres.NewUnitOfWork(db.App, postgres.DefaultTxConfig(), postgres.DefaultRetryPolicy())
	return db
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func requireSQLState(t *testing.T, err error, want string) {
	t.Helper()
	if got := sqlState(err); got != want {
		t.Fatalf("SQLSTATE = %q (err %v), want %q", got, err, want)
	}
}

var brl = must(money.ParseCurrency("BRL"))

func brlAmount(s string) money.Money { return must(money.Parse(s, brl)) }

// seededWallet is a wallet opened with a positive balance through the repositories.
type seededWallet struct {
	ID        wallet.ID
	PlayerID  wallet.PlayerID
	OpeningID wagering.ID
}

func seedWallet(t *testing.T, db *testDB, initial string) seededWallet {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := seededWallet{
		ID:        must(wallet.NewID(uuid.New())),
		PlayerID:  must(wallet.NewPlayerID(uuid.New())),
		OpeningID: must(wagering.NewID(uuid.New())),
	}
	amount := brlAmount(initial)
	err := db.UoW.DoRaw(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		w, mv, err := wallet.Open(s.ID, s.PlayerID, amount, now)
		if err != nil {
			return err
		}
		if err := postgres.NewWalletRepository(tx).Insert(ctx, w); err != nil {
			return err
		}
		opening, err := wagering.NewOpening(s.OpeningID, s.ID, s.PlayerID, amount, "seed", now)
		if err != nil {
			return err
		}
		if err := opening.MarkProcessed(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, nil, now); err != nil {
			return err
		}
		if err := postgres.NewTransactionRepository(tx).Insert(ctx, opening); err != nil {
			return err
		}
		entry, err := wallet.NewLedgerEntry(must(wallet.NewEntryID(uuid.New())), s.ID, s.OpeningID.UUID(), *mv, now)
		if err != nil {
			return err
		}
		return postgres.NewLedgerRepository(tx).Insert(ctx, entry)
	})
	if err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return s
}

func newBet(t *testing.T, w seededWallet, amount string) *wagering.Transaction {
	t.Helper()
	bet, err := wagering.NewExternal(wagering.ExternalParams{
		ID: must(wagering.NewID(uuid.New())), Channel: wagering.ChannelHTTP,
		WalletID: w.ID, PlayerID: w.PlayerID, Kind: wagering.KindBet, Amount: brlAmount(amount),
		ProviderID: "provider-a", ExternalID: "ext-" + uuid.NewString(), IdempotencyKey: "idem-" + uuid.NewString(),
		RoundID: "round-1", GameID: "game-1", CorrelationID: "corr-" + uuid.NewString(),
		Now: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	return bet
}
