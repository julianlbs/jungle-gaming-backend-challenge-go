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

func TestReplayReturnsTheOriginalBalance(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	first := wager(w, "BET", "30.00", "bet-1")
	out, err := f.processor.Process(ctx, first)
	requireOutcome(t, out, err, wagering.StatusProcessed, "70.00", false)
	second, err := f.processor.Process(ctx, wager(w, "BET", "10.00", "bet-2"))
	requireOutcome(t, second, err, wagering.StatusProcessed, "60.00", false)

	replay, err := f.processor.Process(ctx, first)
	requireOutcome(t, replay, err, wagering.StatusProcessed, "70.00", true)
	if replay.Transaction.ID() != out.Transaction.ID() {
		t.Fatal("replay returned a different transaction")
	}
	var minor int64
	if err := f.db.App.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id = $1`, w.ID().UUID()).Scan(&minor); err != nil || minor != 6000 {
		t.Fatalf("stored balance = %d %v, want 6000", minor, err)
	}
}

func (f *appFixture) walletState(t *testing.T, w *wallet.Wallet) (ledger int, version int64) {
	t.Helper()
	err := f.db.App.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1), (SELECT version FROM wallets WHERE id = $1)`,
		w.ID().UUID()).Scan(&ledger, &version)
	if err != nil {
		t.Fatal(err)
	}
	return ledger, version
}

func eventsFor(t *testing.T, f *appFixture, eventType string, id wagering.ID) int {
	t.Helper()
	return countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE event_type = $1
		AND (aggregate_id::text = $2 OR payload->'data'->>'transactionId' = $2)`, eventType, id.String())
}

func TestLossWritesNoLedgerAndNoBalanceEvent(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	ledgerBefore, versionBefore := f.walletState(t, w)
	bet, err := f.processor.Process(ctx, wager(w, "BET", "10.00", "bet-1"))
	requireOutcome(t, bet, err, wagering.StatusProcessed, "90.00", false)
	if n := eventsFor(t, f, "WagerTransactionProcessed", bet.Transaction.ID()); n != 1 {
		t.Fatalf("bet processed events = %d", n)
	}
	if n := eventsFor(t, f, "WalletBalanceChanged", bet.Transaction.ID()); n != 1 {
		t.Fatalf("bet balance events = %d", n)
	}
	var (
		direction                   string
		amount, balBefore, balAfter int64
		entryVersion, walletVersion int64
	)
	if err := f.db.App.QueryRow(ctx, `SELECT e.direction, e.amount_minor, e.balance_before_minor, e.balance_after_minor, e.wallet_version, w.version
		FROM wallet_ledger_entries e JOIN wallets w ON w.id = e.wallet_id WHERE e.transaction_id = $1`,
		bet.Transaction.ID().UUID()).Scan(&direction, &amount, &balBefore, &balAfter, &entryVersion, &walletVersion); err != nil {
		t.Fatal(err)
	}
	if direction != "DEBIT" || amount != 1000 || balBefore != 10000 || balAfter != 9000 ||
		entryVersion != versionBefore+1 || walletVersion != versionBefore+1 {
		t.Fatalf("bet entry = %s %d %d->%d v%d (wallet v%d, before v%d)", direction, amount, balBefore, balAfter, entryVersion, walletVersion, versionBefore)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'
		AND payload->'data'->>'transactionId' = $1 AND payload->'data'->>'direction' = 'DEBIT'
		AND payload->'data'->'balanceBefore'->>'amount' = '100.00' AND payload->'data'->'balanceAfter'->>'amount' = '90.00'`,
		bet.Transaction.ID().String()); n != 1 {
		t.Fatal("bet balance event does not describe the debit")
	}
	if ledger, _ := f.walletState(t, w); ledger != ledgerBefore+1 {
		t.Fatalf("ledger entries after bet = %d, want %d", ledger, ledgerBefore+1)
	}

	ledgerBefore, versionBefore = f.walletState(t, w)
	loss, err := f.processor.Process(ctx, wager(w, "LOSS", "0", "loss-1"))
	requireOutcome(t, loss, err, wagering.StatusProcessed, "90.00", false)
	if ledger, version := f.walletState(t, w); ledger != ledgerBefore || version != versionBefore {
		t.Fatalf("loss moved the wallet: ledger %d->%d version %d->%d", ledgerBefore, ledger, versionBefore, version)
	}
	if n := eventsFor(t, f, "WagerTransactionProcessed", loss.Transaction.ID()); n != 1 {
		t.Fatalf("loss processed events = %d", n)
	}
	if n := eventsFor(t, f, "WalletBalanceChanged", loss.Transaction.ID()); n != 0 {
		t.Fatalf("loss balance events = %d", n)
	}
	if got := f.balance(t, w); got != "90.00" {
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
