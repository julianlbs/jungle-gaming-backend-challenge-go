//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/postgres"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/snspublisher"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/sqsconsumer"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func startConsumer(t *testing.T, a awsClients, h sqsconsumer.Handler, queueURL, dlqURL string) {
	t.Helper()
	c := sqsconsumer.New(a.sqs, h, sqsconsumer.Config{
		QueueURL: queueURL, DLQURL: dlqURL, AllowedProviders: []string{"provider-a"},
		MaxInFlight: 4, MessageTimeout: 10 * time.Second, WaitTime: time.Second,
	}, quietLog, metrics.New())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestConsumerRedeliveryAndDeadLetters(t *testing.T) {
	f := newAppFixture(t)
	a := newAWS(t)
	dlq := a.fifoQueue(t, "dlq", "", 0)
	queue := a.fifoQueue(t, "wagers", dlq, 5)
	startConsumer(t, a, app.NewWagerIntake(f.db.UoW, f.processor, app.SystemClock{}), queue, dlq)

	w := f.openWallet(t, "100.00")
	group := w.ID().String()
	body := must(sqsconsumer.Encode("msg-1", "2026-09-08T12:00:00Z", "corr-1", wager(w, "BET", "10.00", "ext-1")))

	a.send(t, queue, group, "d1", body)
	waitFor(t, 15*time.Second, "bet applied", func() bool { return f.balance(t, w) == "90.00" })

	// A redelivery that bypasses SQS deduplication is absorbed by the inbox.
	a.send(t, queue, group, "d2", body)
	waitFor(t, 15*time.Second, "redelivery handled", func() bool {
		return countRows(t, f.db, `SELECT count(*) FROM inbox_messages WHERE message_id = 'msg-1'`) == 1 &&
			a.queueEmpty(t, queue)
	})
	if f.balance(t, w) != "90.00" {
		t.Fatalf("redelivery changed the balance: %s", f.balance(t, w))
	}

	a.send(t, queue, group, "d3", []byte(`{"messageId":"broken"`))
	foreign := must(sqsconsumer.Encode("msg-2", "2026-09-08T12:00:00Z", "", func() app.WagerCommand {
		c := wager(w, "BET", "1.00", "ext-2")
		c.ProviderID = "provider-z"
		return c
	}()))
	a.send(t, queue, group, "d4", foreign)
	conflict := must(sqsconsumer.Encode("msg-1", "2026-09-08T12:00:00Z", "", wager(w, "BET", "99.00", "ext-1")))
	a.send(t, queue, group, "d5", conflict)

	dead := a.drain(t, dlq, 3, 20*time.Second)
	reasons := map[string]bool{}
	for _, m := range dead {
		reasons[aws.ToString(m.MessageAttributes["failureReason"].StringValue)] = true
	}
	for _, want := range []string{"INVALID_MESSAGE", "PROVIDER_NOT_ALLOWED", "MESSAGE_CONFLICT"} {
		if !reasons[want] {
			t.Errorf("missing dead letter %s; got %v", want, reasons)
		}
	}
	if len(dead) != 3 || f.balance(t, w) != "90.00" {
		t.Fatalf("dead letters = %d, balance = %s", len(dead), f.balance(t, w))
	}
}

type unavailableHandler struct{ calls atomic.Int32 }

func (h *unavailableHandler) Handle(context.Context, app.IncomingWager) (app.IntakeResult, error) {
	h.calls.Add(1)
	return app.IntakeResult{}, app.ErrUnavailable
}

func TestTransientFailuresRedriveToDeadLetterQueue(t *testing.T) {
	f := newAppFixture(t)
	a := newAWS(t)
	dlq := a.fifoQueue(t, "dlq", "", 0)
	queue := a.fifoQueue(t, "wagers", dlq, 2)
	h := &unavailableHandler{}
	startConsumer(t, a, h, queue, dlq)

	w := f.openWallet(t, "100.00")
	a.send(t, queue, w.ID().String(), "d1", must(sqsconsumer.Encode("msg-1", "2026-09-08T12:00:00Z", "", wager(w, "BET", "1.00", "ext-1"))))

	dead := a.drain(t, dlq, 1, 30*time.Second)
	if len(dead) != 1 {
		t.Fatalf("dead letters = %d", len(dead))
	}
	if n := h.calls.Load(); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

// crashAfterPublish publishes and then reports a failure, as if the process died before
// confirming the outbox row.
type crashAfterPublish struct {
	inner app.EventPublisher
	once  sync.Map
}

func (p *crashAfterPublish) Publish(ctx context.Context, ev app.ClaimedEvent) error {
	if err := p.inner.Publish(ctx, ev); err != nil {
		return err
	}
	if _, crashed := p.once.LoadOrStore(ev.ID, true); !crashed {
		return errors.New("crashed before confirming")
	}
	return nil
}

func TestCompetingRelaysPublishEveryEventOnce(t *testing.T) {
	f := newAppFixture(t)
	a := newAWS(t)
	topic, audit := a.fifoTopic(t)
	ctx := context.Background()

	for i := range 4 {
		w := f.openWallet(t, "100.00")
		for j := range 3 {
			if _, err := f.processor.Process(ctx, wager(w, "BET", "1.00", fmt.Sprintf("ext-%d-%d", i, j))); err != nil {
				t.Fatal(err)
			}
		}
	}
	total := countRows(t, f.db, `SELECT count(*) FROM outbox_events`)

	store := postgres.NewOutboxStore(f.db.App)
	// An abandoned lease: claimed by a publisher that died before doing anything.
	abandoned := must(store.Claim(ctx, "dead-instance", time.Second, 2))
	if len(abandoned) != 2 {
		t.Fatalf("abandoned = %d", len(abandoned))
	}

	publisher := snspublisher.New(a.sns, topic)
	var wg sync.WaitGroup
	for i := range 3 {
		var p app.EventPublisher = publisher
		if i == 0 {
			p = &crashAfterPublish{inner: publisher}
		}
		relay := app.NewOutboxRelay(store, p, app.RelayConfig{
			Owner: fmt.Sprintf("relay-%d", i), Lease: 10 * time.Second, BatchSize: 3,
			BaseDelay: 200 * time.Millisecond, MaxDelay: time.Second,
		}, nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0 {
					return
				}
				if _, err := relay.RunOnce(ctx); err != nil {
					t.Error(err)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if n := countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`); n != 0 {
		t.Fatalf("%d events left unpublished", n)
	}
	got := a.drain(t, audit, total, 20*time.Second)
	seen := map[string]int{}
	for _, m := range got {
		seen[eventID(t, m)]++
	}
	if len(seen) != total {
		t.Fatalf("distinct events delivered = %d, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %s delivered %d times", id, n)
		}
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM outbox_events WHERE attempts > 1`); n == 0 {
		t.Fatal("expected republished events after the simulated crash")
	}
}
