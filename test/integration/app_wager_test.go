//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

type appFixture struct {
	db        *testDB
	opener    *app.WalletOpener
	processor *app.WagerProcessor
}

func newAppFixture(t *testing.T) *appFixture {
	db := newTestDB(t)
	return &appFixture{
		db:        db,
		opener:    app.NewWalletOpener(db.UoW, app.SystemClock{}, app.TimeOrderedIDs{}),
		processor: app.NewWagerProcessor(db.UoW, app.SystemClock{}, app.TimeOrderedIDs{}, app.DefaultPendingPolicy(), nil),
	}
}

func (f *appFixture) openWallet(t *testing.T, amount string) *wallet.Wallet {
	t.Helper()
	w, err := f.opener.Open(context.Background(), app.OpenWalletCommand{
		PlayerID: uuid.NewString(), Currency: "BRL", InitialAmount: amount, CorrelationID: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func wager(w *wallet.Wallet, kind, amount, externalID string) app.WagerCommand {
	return app.WagerCommand{
		Channel: wagering.ChannelHTTP, ProviderID: "provider-a",
		ExternalTransactionID: externalID, IdempotencyKey: "key-" + externalID,
		WalletID: w.ID().String(), PlayerID: w.PlayerID().String(),
		Kind: kind, Amount: amount, Currency: "BRL",
		RoundID: "round-1", GameID: "game-1", CorrelationID: "corr-" + externalID,
	}
}

func postgresReadModel(f *appFixture) *postgres.ReadModel { return postgres.NewReadModel(f.db.App) }

func (f *appFixture) balance(t *testing.T, w *wallet.Wallet) string {
	t.Helper()
	var minor int64
	if err := f.db.App.QueryRow(context.Background(), `SELECT balance_minor FROM wallets WHERE id = $1`, w.ID().UUID()).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	return must(brlFromMinor(minor)).String()
}

func requireOutcome(t *testing.T, out app.WagerOutcome, err error, status wagering.Status, balance string, replay bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tx := out.Transaction
	if tx.Status() != status || out.Replay != replay {
		t.Fatalf("status=%s replay=%v, want %s replay=%v (failure %s)", tx.Status(), out.Replay, status, replay, tx.FailureCode())
	}
	if balance != "" {
		r, ok := tx.Result()
		if !ok || r.Balance.String() != balance {
			t.Fatalf("result balance = %v, want %s", r.Balance, balance)
		}
	}
}

func TestWagerBetReplayAndConflicts(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	cmd := wager(w, "BET", "30.00", "bet-1")
	out, err := f.processor.Process(ctx, cmd)
	requireOutcome(t, out, err, wagering.StatusProcessed, "70.00", false)

	again, err := f.processor.Process(ctx, cmd)
	requireOutcome(t, again, err, wagering.StatusProcessed, "70.00", true)
	if again.Transaction.ID() != out.Transaction.ID() {
		t.Fatal("replay returned a different transaction")
	}

	changed := cmd
	changed.Amount = "31.00"
	if _, err := f.processor.Process(ctx, changed); !errors.Is(err, app.ErrIdempotencyKeyReused) {
		t.Fatalf("changed payload: %v", err)
	}
	otherKey := cmd
	otherKey.IdempotencyKey = "another-key"
	if _, err := f.processor.Process(ctx, otherKey); !errors.Is(err, app.ErrExternalTransactionConflict) {
		t.Fatalf("reused external id: %v", err)
	}
	if got := f.balance(t, w); got != "70.00" {
		t.Fatalf("balance = %s", got)
	}
}

func TestConcurrentBetsNeverOverdraw(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[wagering.Status]int{}
	)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := f.processor.Process(ctx, wager(w, "BET", "80.00", "concurrent-"+string(rune('a'+i))))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			statuses[out.Transaction.Status()]++
			mu.Unlock()
			if out.Transaction.Status() == wagering.StatusRejected && out.Transaction.FailureCode() != wagering.FailureInsufficientFunds {
				t.Errorf("failure code = %s", out.Transaction.FailureCode())
			}
		}()
	}
	wg.Wait()
	if statuses[wagering.StatusProcessed] != 1 || statuses[wagering.StatusRejected] != 1 {
		t.Fatalf("statuses = %v", statuses)
	}
	if got := f.balance(t, w); got != "20.00" {
		t.Fatalf("balance = %s", got)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID().UUID()); n != 2 {
		t.Fatalf("ledger entries = %d", n)
	}
}

func TestReversalWaitsForReference(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	refund := wager(w, "REFUND", "10.00", "refund-1")
	refund.ReferenceExternalTransactionID = "bet-late"
	out, err := f.processor.Process(ctx, refund)
	requireOutcome(t, out, err, wagering.StatusPendingReference, "", false)
	if f.balance(t, w) != "100.00" {
		t.Fatal("pending refund changed the balance")
	}
	replay, err := f.processor.Process(ctx, refund)
	requireOutcome(t, replay, err, wagering.StatusPendingReference, "", true)
}

func TestInvalidWagersAreNotPersisted(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	cases := map[string]struct {
		mutate func(*app.WagerCommand)
		want   error
	}{
		"opening kind":   {func(c *app.WagerCommand) { c.Kind = "OPENING" }, wagering.ErrReservedKind},
		"unknown kind":   {func(c *app.WagerCommand) { c.Kind = "JACKPOT" }, wagering.ErrInvalidField},
		"bad amount":     {func(c *app.WagerCommand) { c.Amount = "1.234" }, wagering.ErrInvalidField},
		"zero bet":       {func(c *app.WagerCommand) { c.Amount = "0" }, wagering.ErrInvalidField},
		"bad currency":   {func(c *app.WagerCommand) { c.Currency = "XYZ" }, wagering.ErrInvalidField},
		"missing wallet": {func(c *app.WagerCommand) { c.WalletID = uuid.NewString() }, app.ErrWalletNotFound},
		"other player":   {func(c *app.WagerCommand) { c.PlayerID = uuid.NewString() }, app.ErrWalletMismatch},
		"other currency": {func(c *app.WagerCommand) { c.Currency = "USD" }, app.ErrWalletMismatch},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := wager(w, "BET", "5.00", "invalid-"+uuid.NewString())
			tc.mutate(&cmd)
			if _, err := f.processor.Process(ctx, cmd); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM wager_transactions WHERE origin = 'EXTERNAL'`); n != 0 {
		t.Fatalf("persisted %d invalid transactions", n)
	}
}
