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
	return f, app.NewPendingResumer(f.db.UoW, store, app.SystemClock{}, app.TimeOrderedIDs{}, policy, store)
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
