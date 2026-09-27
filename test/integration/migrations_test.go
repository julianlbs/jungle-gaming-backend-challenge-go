//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
)

var expectedTables = []string{"inbox_messages", "outbox_events", "wager_transactions", "wallet_ledger_entries", "wallets"}

func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	name := "wallet_mig_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if err := createEmptyDatabase(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dropDatabase(context.Background(), name) })
	url := withDatabase(env.ownerURL, name)

	run := func(step func(*postgres.Migrator) error) {
		t.Helper()
		m, err := postgres.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := step(m); err != nil {
			t.Fatal(err)
		}
	}
	tables := func() []string {
		t.Helper()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		rows, _ := conn.Query(ctx, `SELECT tablename FROM pg_tables
			WHERE schemaname = 'public' AND tablename <> 'schema_migrations' ORDER BY tablename`)
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	version := func() uint {
		t.Helper()
		var v uint
		run(func(m *postgres.Migrator) error {
			var dirty bool
			var err error
			v, dirty, err = m.Version()
			if dirty {
				t.Fatal("schema left dirty")
			}
			return err
		})
		return v
	}

	run((*postgres.Migrator).Up)
	if got := tables(); strings.Join(got, ",") != strings.Join(expectedTables, ",") {
		t.Fatalf("tables after up: %v", got)
	}
	if v := version(); v != 5 {
		t.Fatalf("version after up = %d", v)
	}
	run((*postgres.Migrator).Up)

	run(func(m *postgres.Migrator) error { return m.Down(0) })
	if got := tables(); len(got) != 0 {
		t.Fatalf("tables after down: %v", got)
	}
	if v := version(); v != 0 {
		t.Fatalf("version after down = %d", v)
	}

	run((*postgres.Migrator).Up)
	if v := version(); v != 5 {
		t.Fatalf("version after second up = %d", v)
	}
}
