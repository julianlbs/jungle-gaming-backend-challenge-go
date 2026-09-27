//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/money"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wallet"
)

func TestRepositoriesRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	w := seedWallet(t, db, "100.00")

	err := db.UoW.DoRaw(ctx, func(ctx context.Context, tx pgx.Tx) error {
		dup, _, _ := wallet.Open(must(wallet.NewID(uuid.New())), w.PlayerID, must(money.Zero(brl)), time.Now())
		return postgres.NewWalletRepository(tx).Insert(ctx, dup)
	})
	if !errors.Is(err, app.ErrWalletAlreadyExists) {
		t.Fatalf("duplicate wallet: %v", err)
	}

	bet := newBet(t, w, "10.50")
	err = db.UoW.DoRaw(ctx, func(ctx context.Context, tx pgx.Tx) error {
		wallets, txs, ledger := postgres.NewWalletRepository(tx), postgres.NewTransactionRepository(tx), postgres.NewLedgerRepository(tx)
		if err := txs.Insert(ctx, bet); err != nil {
			return err
		}
		wl, err := wallets.GetForUpdate(ctx, w.ID)
		if err != nil {
			return err
		}
		prev := wl.Version()
		now := time.Now().UTC()
		mv, err := wl.Debit(bet.Amount(), now)
		if err != nil {
			return err
		}
		if err := wallets.UpdateBalance(ctx, wl, prev); err != nil {
			return err
		}
		entry, err := wallet.NewLedgerEntry(must(wallet.NewEntryID(uuid.New())), w.ID, bet.ID().UUID(), mv, now)
		if err != nil {
			return err
		}
		if err := ledger.Insert(ctx, entry); err != nil {
			return err
		}
		if err := bet.MarkProcessed(wagering.Result{Balance: wl.Balance(), WalletVersion: wl.Version()}, nil, now); err != nil {
			return err
		}
		return txs.Update(ctx, bet)
	})
	if err != nil {
		t.Fatalf("bet: %v", err)
	}

	ext, _ := bet.External()
	txs := postgres.NewTransactionRepository(db.App)
	got, err := txs.FindByIdempotencyKey(ctx, ext.ProviderID, ext.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != bet.ID() || got.Status() != wagering.StatusProcessed {
		t.Fatalf("bet read back: %+v", got.Snapshot())
	}
	if r, ok := got.Result(); !ok || r.Balance.String() != "89.50" || r.WalletVersion != 2 {
		t.Fatalf("bet result: %+v", r)
	}
	if byExt, err := txs.FindByExternalID(ctx, ext.ProviderID, ext.ExternalID); err != nil || byExt.ID() != bet.ID() {
		t.Fatalf("find by external id: %v", err)
	}
	if _, err := txs.FindByExternalID(ctx, ext.ProviderID, "missing"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing lookup: %v", err)
	}

	wallets := postgres.NewWalletRepository(db.App)
	wl, err := wallets.Get(ctx, w.ID)
	if err != nil || wl.Balance().String() != "89.50" || wl.Version() != 2 {
		t.Fatalf("wallet: %v %v", wl, err)
	}
	if byPlayer, err := wallets.FindByPlayer(ctx, w.PlayerID, brl); err != nil || byPlayer.ID() != w.ID {
		t.Fatalf("find by player: %v", err)
	}
	if err := wallets.UpdateBalance(ctx, wl, 1); !errors.Is(err, app.ErrConcurrentUpdate) {
		t.Fatalf("stale version update: %v", err)
	}

	entries, err := postgres.NewLedgerRepository(db.App).ListByWallet(ctx, w.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Direction() != wallet.DirectionCredit || entries[1].BalanceAfter().String() != "89.50" {
		t.Fatalf("ledger: %+v", entries)
	}
	if page, _ := postgres.NewLedgerRepository(db.App).ListByWallet(ctx, w.ID, 1, 10); len(page) != 1 {
		t.Fatalf("ledger page after version 1: %d", len(page))
	}
}

func TestPendingReferenceRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	w := seedWallet(t, db, "50.00")
	now := time.Now().UTC().Truncate(time.Microsecond)

	refund, err := wagering.NewExternal(wagering.ExternalParams{
		ID: must(wagering.NewID(uuid.New())), Channel: wagering.ChannelSQS,
		WalletID: w.ID, PlayerID: w.PlayerID, Kind: wagering.KindRefund, Amount: brlAmount("5.00"),
		ProviderID: "provider-a", ExternalID: "refund-1", IdempotencyKey: "idem-refund-1",
		RoundID: "round-1", GameID: "game-1", ReferenceExternalID: "bet-late", CorrelationID: "corr-r", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refund.AwaitReference(now.Add(time.Second), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	txs := postgres.NewTransactionRepository(db.App)
	if err := txs.Insert(ctx, refund); err != nil {
		t.Fatal(err)
	}
	waiting, err := txs.ListWaitingFor(ctx, "provider-a", "bet-late")
	if err != nil || len(waiting) != 1 || waiting[0].ID() != refund.ID() {
		t.Fatalf("waiting: %v %v", waiting, err)
	}
	s := waiting[0].Snapshot()
	if s.Status != wagering.StatusPendingReference || !s.Deadline.Equal(now.Add(time.Minute)) ||
		s.External.ReferenceExternalID != "bet-late" || s.Channel != wagering.ChannelSQS {
		t.Fatalf("pending snapshot: %+v", s)
	}
}
