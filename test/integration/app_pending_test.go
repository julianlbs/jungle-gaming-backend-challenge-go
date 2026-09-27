//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

func fastPendingPolicy() app.PendingPolicy {
	return app.PendingPolicy{BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond, MaxAttempts: 1000, TTL: 400 * time.Millisecond}
}

func newPendingFixture(t *testing.T) (*appFixture, *app.PendingResumer) {
	f := newAppFixture(t)
	store := postgres.NewPendingStore(f.db.App)
	policy := fastPendingPolicy()
	f.processor = app.NewWagerProcessor(f.db.UoW, app.SystemClock{}, app.TimeOrderedIDs{}, policy, store)
	return f, app.NewPendingResumer(f.db.UoW, store, app.SystemClock{}, app.TimeOrderedIDs{}, policy, store, nil)
}

// drainUntil runs the resumer until the transaction leaves PENDING_REFERENCE.
func drainUntil(t *testing.T, f *appFixture, r *app.PendingResumer, id wagering.ID) *wagering.Transaction {
	t.Helper()
	ctx := context.Background()
	txs := postgres.NewTransactionRepository(f.db.App)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.RunOnce(ctx, time.Second, 10); err != nil {
			t.Fatal(err)
		}
		got, err := txs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status() != wagering.StatusPendingReference {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("transaction still pending")
	return nil
}

func TestRefundBeforeBetIsAppliedWhenBetArrives(t *testing.T) {
	f, resumer := newPendingFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	refund := wager(w, "REFUND", "25.00", "refund-early")
	refund.ReferenceExternalTransactionID = "bet-late"
	out, err := f.processor.Process(ctx, refund)
	requireOutcome(t, out, err, wagering.StatusPendingReference, "", false)

	bet, err := f.processor.Process(ctx, wager(w, "BET", "25.00", "bet-late"))
	requireOutcome(t, bet, err, wagering.StatusProcessed, "75.00", false)

	done := drainUntil(t, f, resumer, out.Transaction.ID())
	if done.Status() != wagering.StatusProcessed {
		t.Fatalf("refund %s %s", done.Status(), done.FailureCode())
	}
	if ref, ok := done.ReferenceID(); !ok || ref != bet.Transaction.ID() {
		t.Fatal("refund not linked to the bet")
	}
	if got := f.balance(t, w); got != "100.00" {
		t.Fatalf("balance = %s", got)
	}

	replay, err := f.processor.Process(ctx, refund)
	requireOutcome(t, replay, err, wagering.StatusProcessed, "100.00", true)
}

func TestPendingPermanentFailureDoesNotMoveMoney(t *testing.T) {
	f, resumer := newPendingFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	refund := wager(w, "REFUND", "10.00", "refund-early")
	refund.ReferenceExternalTransactionID = "bet-late"
	out, err := f.processor.Process(ctx, refund)
	requireOutcome(t, out, err, wagering.StatusPendingReference, "", false)
	bet, err := f.processor.Process(ctx, wager(w, "BET", "10.00", "bet-late"))
	requireOutcome(t, bet, err, wagering.StatusProcessed, "90.00", false)
	// Fills the balance up to the largest representable amount so that crediting the refund overflows.
	win := wager(w, "WIN", "92233720368547668.07", "win-max")
	win.ReferenceExternalTransactionID = "bet-late"
	full, err := f.processor.Process(ctx, win)
	requireOutcome(t, full, err, wagering.StatusProcessed, "92233720368547758.07", false)

	ledger := func() int {
		return countRows(t, f.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID().UUID())
	}
	before := ledger()
	done := drainUntil(t, f, resumer, out.Transaction.ID())
	if done.Status() != wagering.StatusFailed || done.FailureCode() != wagering.FailureProcessingFailed {
		t.Fatalf("got %s %s", done.Status(), done.FailureCode())
	}
	if got := done.FailureDetail(); got != "processing failed" {
		t.Fatalf("failureDetail = %q", got)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'FAILED'`, out.Transaction.ID().UUID()); n != 1 {
		t.Fatal("failure not persisted")
	}
	if got := f.balance(t, w); got != "92233720368547758.07" {
		t.Fatalf("balance = %s", got)
	}
	if n := ledger(); n != before {
		t.Fatalf("ledger entries %d -> %d", before, n)
	}

	if _, err := resumer.RunOnce(ctx, time.Second, 10); err != nil {
		t.Fatal(err)
	}
	after, err := postgres.NewTransactionRepository(f.db.App).Get(ctx, out.Transaction.ID())
	if err != nil || after.Status() != wagering.StatusFailed {
		t.Fatalf("after another pass: %v %v", after.Status(), err)
	}
	if n := ledger(); n != before {
		t.Fatalf("another pass wrote ledger entries: %d -> %d", before, n)
	}
	if got := f.balance(t, w); got != "92233720368547758.07" {
		t.Fatalf("balance after another pass = %s", got)
	}
}

func TestPendingReferenceExpires(t *testing.T) {
	f, resumer := newPendingFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	refund := wager(w, "REFUND", "5.00", "refund-orphan")
	refund.ReferenceExternalTransactionID = "never-arrives"
	out, err := f.processor.Process(ctx, refund)
	requireOutcome(t, out, err, wagering.StatusPendingReference, "", false)

	done := drainUntil(t, f, resumer, out.Transaction.ID())
	if done.Status() != wagering.StatusRejected || done.FailureCode() != wagering.FailureReferenceNotFound {
		t.Fatalf("got %s %s", done.Status(), done.FailureCode())
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE aggregate_id = $1 AND event_type = 'WagerTransactionRejected'`,
		out.Transaction.ID().UUID()); n != 1 {
		t.Fatalf("rejection events = %d", n)
	}
	if got := f.balance(t, w); got != "100.00" {
		t.Fatalf("balance = %s", got)
	}
}
