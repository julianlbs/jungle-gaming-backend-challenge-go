//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func countRows(t *testing.T, db *testDB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.App.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenWallet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	opener := app.NewWalletOpener(db.UoW, app.SystemClock{}, app.TimeOrderedIDs{})

	player := uuid.NewString()
	w, err := opener.Open(ctx, app.OpenWalletCommand{PlayerID: player, Currency: "BRL", InitialAmount: "100.00", CorrelationID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().String() != "100.00" || w.Version() != 1 {
		t.Fatalf("wallet %s v%d", w.Balance(), w.Version())
	}
	id := w.ID().UUID()
	if n := countRows(t, db, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED'`, id); n != 1 {
		t.Fatalf("opening transactions = %d", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT' AND amount_minor = 10000`, id); n != 1 {
		t.Fatalf("ledger entries = %d", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM outbox_events WHERE partition_key = $1`, id.String()); n != 2 {
		t.Fatalf("outbox events = %d", n)
	}

	_, err = opener.Open(ctx, app.OpenWalletCommand{PlayerID: player, Currency: "BRL", CorrelationID: "c2"})
	if !errors.Is(err, app.ErrWalletAlreadyExists) {
		t.Fatalf("duplicate: %v", err)
	}

	empty, err := opener.Open(ctx, app.OpenWalletCommand{PlayerID: player, Currency: "USD", CorrelationID: "c3"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Balance().String() != "0.00" || empty.Version() != 1 {
		t.Fatalf("empty wallet %s v%d", empty.Balance(), empty.Version())
	}
	if n := countRows(t, db, `SELECT count(*) FROM outbox_events WHERE partition_key = $1`, empty.ID().String()); n != 0 {
		t.Fatalf("zero-balance wallet emitted %d events", n)
	}

	for _, cmd := range []app.OpenWalletCommand{
		{PlayerID: "nope", Currency: "BRL", CorrelationID: "c"},
		{PlayerID: player, Currency: "JPY", CorrelationID: "c"},
		{PlayerID: player, Currency: "EUR", InitialAmount: "-1", CorrelationID: "c"},
		{PlayerID: player, Currency: "EUR", InitialAmount: "1.001", CorrelationID: "c"},
	} {
		if _, err := opener.Open(ctx, cmd); !errors.Is(err, wagering.ErrInvalidField) {
			t.Errorf("%+v: %v", cmd, err)
		}
	}
}
