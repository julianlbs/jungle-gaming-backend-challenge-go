//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func TestLedgerPaginationAndVisibility(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	q := app.NewQueries(postgres.NewReadModel(f.db.App), app.SystemClock{})
	w := f.openWallet(t, "100.00")

	var last app.WagerOutcome
	for i := range 4 {
		out, err := f.processor.Process(ctx, wager(w, "BET", "1.00", fmt.Sprintf("page-%d", i)))
		requireOutcome(t, out, err, wagering.StatusProcessed, "", false)
		last = out
	}

	var versions []int64
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := q.Ledger(ctx, w.ID().String(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			versions = append(versions, e.WalletVersion())
		}
		if page.NextCursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("pagination does not terminate")
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(versions) != "[1 2 3 4 5]" {
		t.Fatalf("versions = %v", versions)
	}
	if _, err := q.Ledger(ctx, w.ID().String(), "!!", 2); !errors.Is(err, wagering.ErrInvalidField) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, err := q.Ledger(ctx, w.ID().String(), "", 500); !errors.Is(err, wagering.ErrInvalidField) {
		t.Fatalf("bad limit: %v", err)
	}
	if _, err := q.Ledger(ctx, uuid.NewString(), "", 10); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unknown wallet: %v", err)
	}

	id := last.Transaction.ID().String()
	if _, err := q.Transaction(ctx, id, app.Viewer{ProviderID: "provider-a"}); err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if _, err := q.Transaction(ctx, id, app.Viewer{ProviderID: "provider-b"}); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("other provider read: %v", err)
	}
	if _, err := q.Transaction(ctx, id, app.Viewer{ReadAll: true}); err != nil {
		t.Fatalf("read-all: %v", err)
	}
	if got, err := q.TransactionByExternalID(ctx, "provider-a", "page-3"); err != nil || got.ID() != last.Transaction.ID() {
		t.Fatalf("by external id: %v", err)
	}
}

func TestReconciliation(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	q := app.NewQueries(postgres.NewReadModel(f.db.App), app.SystemClock{})
	w := f.openWallet(t, "100.00")
	_, err := f.processor.Process(ctx, wager(w, "BET", "40.00", "rec-bet"))
	if err != nil {
		t.Fatal(err)
	}
	win := wager(w, "WIN", "15.50", "rec-win")
	win.ReferenceExternalTransactionID = "rec-bet"
	if _, err := f.processor.Process(ctx, win); err != nil {
		t.Fatal(err)
	}

	rec, err := q.Reconcile(ctx, w.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Consistent || rec.Stored.String() != "75.50" || rec.Calculated.String() != "75.50" || rec.EntryCount != 3 {
		t.Fatalf("reconciliation = %+v", rec)
	}

	empty, err := f.opener.Open(ctx, app.OpenWalletCommand{PlayerID: uuid.NewString(), Currency: "BRL", CorrelationID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := q.Reconcile(ctx, empty.ID().String()); err != nil || !rec.Consistent || rec.EntryCount != 0 {
		t.Fatalf("empty wallet reconciliation = %+v %v", rec, err)
	}

	// Only the schema owner can bypass the guards; this simulates out-of-band tampering.
	if _, err := f.db.Owner.Exec(ctx, `ALTER TABLE wallets DISABLE TRIGGER USER;
		UPDATE wallets SET balance_minor = 999999 WHERE id = '`+w.ID().String()+`';
		ALTER TABLE wallets ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	rec, err = q.Reconcile(ctx, w.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Consistent || rec.Difference.String() != "9924.49" {
		t.Fatalf("tampered reconciliation = %+v", rec)
	}
}
