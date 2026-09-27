//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/event"
)

func TestOutboxLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	pool := db.App
	store := postgres.NewOutboxStore(pool)

	// Payload with key order and spacing that JSONB would normalize.
	payload := []byte(`{"z":1, "a":{"amount":"10.00","currency":"BRL"}}`)
	ids := map[uuid.UUID]bool{}
	var events []event.Outgoing
	for range 3 {
		id := uuid.New()
		ids[id] = true
		events = append(events, event.Outgoing{
			EventID: id, EventType: event.TypeWalletBalanceChanged, EventVersion: 1,
			AggregateType: event.AggregateWallet, AggregateID: uuid.NewString(), PartitionKey: "w-1",
			CorrelationID: "corr", OccurredAt: time.Now().UTC(), Payload: payload,
		})
	}
	if err := store.Insert(ctx, events...); err != nil {
		t.Fatal(err)
	}

	claimMine := func(owner string) []app.ClaimedEvent {
		batch, err := store.Claim(ctx, owner, time.Minute, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var mine []app.ClaimedEvent
		for _, c := range batch {
			if ids[c.ID] {
				mine = append(mine, c)
			} else if _, err := store.MarkPublished(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		return mine
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed = map[uuid.UUID]string{}
	)
	for _, owner := range []string{"relay-a", "relay-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range claimMine(owner) {
				mu.Lock()
				if prev, dup := claimed[c.ID]; dup {
					t.Errorf("event %s claimed by %s and %s", c.ID, prev, owner)
				}
				claimed[c.ID] = owner
				mu.Unlock()
				if string(c.Payload) != string(payload) || c.Attempts != 1 {
					t.Errorf("claimed payload %q attempts %d", c.Payload, c.Attempts)
				}
			}
		}()
	}
	wg.Wait()
	if len(claimed) != 3 {
		t.Fatalf("claimed %d of 3 events", len(claimed))
	}
	if again := claimMine("relay-c"); len(again) != 0 {
		t.Fatalf("leased events were reclaimed: %d", len(again))
	}

	var first, second, third uuid.UUID
	for id := range ids {
		switch {
		case first == uuid.Nil:
			first = id
		case second == uuid.Nil:
			second = id
		default:
			third = id
		}
	}
	if ok, err := store.MarkPublished(ctx, first); err != nil || !ok {
		t.Fatalf("publish: %v %v", ok, err)
	}
	if ok, _ := store.MarkPublished(ctx, first); ok {
		t.Fatal("event published twice")
	}
	if err := store.Reschedule(ctx, second, claimed[second], 0, errors.New("sns down")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseLeases(ctx, claimed[third]); err != nil {
		t.Fatal(err)
	}
	retry := claimMine("relay-c")
	if len(retry) != 2 {
		t.Fatalf("expected 2 reclaimable events, got %d", len(retry))
	}
	for _, c := range retry {
		if c.Attempts != 2 {
			t.Errorf("attempts after reclaim = %d", c.Attempts)
		}
		if _, err := store.MarkPublished(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET payload = '{}' WHERE id = $1`, second); err == nil {
		t.Fatal("payload update was allowed")
	}
}

func TestInboxDuplicateAndRace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	pool := db.App
	uow := db.UoW

	now := time.Now().UTC()
	entry := app.InboxEntry{
		Consumer: "wager-sqs", MessageID: uuid.NewString(),
		PayloadHash: "sha256:" + "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34",
		Outcome:     app.InboxRejected, ReceivedAt: now, CompletedAt: now,
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		inserted  int
		duplicate int
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := uow.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
				inbox := postgres.NewInboxStore(tx)
				if _, err := inbox.Find(ctx, entry.Consumer, entry.MessageID); err == nil {
					mu.Lock()
					duplicate++
					mu.Unlock()
					return nil
				} else if !errors.Is(err, app.ErrNotFound) {
					return err
				}
				if err := inbox.Insert(ctx, entry); err != nil {
					return err
				}
				mu.Lock()
				inserted++
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if inserted != 1 || duplicate != 3 {
		t.Fatalf("inserted=%d duplicate=%d", inserted, duplicate)
	}

	got, err := postgres.NewInboxStore(pool).Find(ctx, entry.Consumer, entry.MessageID)
	if err != nil || got.PayloadHash != entry.PayloadHash || got.Outcome != app.InboxRejected {
		t.Fatalf("find: %+v %v", got, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM inbox_messages WHERE message_id = $1`, entry.MessageID); err == nil {
		t.Fatal("inbox delete was allowed")
	}
}
