package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeOutbox struct {
	batch       []ClaimedEvent
	published   []uuid.UUID
	rescheduled map[uuid.UUID]time.Duration
	markResult  bool
}

func (f *fakeOutbox) Claim(context.Context, string, time.Duration, int) ([]ClaimedEvent, error) {
	b := f.batch
	f.batch = nil
	return b, nil
}

func (f *fakeOutbox) MarkPublished(_ context.Context, id uuid.UUID) (bool, error) {
	f.published = append(f.published, id)
	return f.markResult, nil
}

func (f *fakeOutbox) Reschedule(_ context.Context, id uuid.UUID, _ string, d time.Duration, _ error) error {
	f.rescheduled[id] = d
	return nil
}

func (f *fakeOutbox) ReleaseLeases(context.Context, string) (int64, error) { return 0, nil }

type fakePublisher struct{ fail map[uuid.UUID]bool }

func (p fakePublisher) Publish(_ context.Context, ev ClaimedEvent) error {
	if p.fail[ev.ID] {
		return errors.New("broker down")
	}
	return nil
}

func TestRelayPublishesAndReschedules(t *testing.T) {
	ok, bad := ClaimedEvent{ID: uuid.New(), Attempts: 1}, ClaimedEvent{ID: uuid.New(), Attempts: 3}
	q := &fakeOutbox{batch: []ClaimedEvent{ok, bad}, rescheduled: map[uuid.UUID]time.Duration{}, markResult: true}
	results := map[RelayResult]int{}
	r := NewOutboxRelay(q, fakePublisher{fail: map[uuid.UUID]bool{bad.ID: true}}, RelayConfig{Owner: "a", BatchSize: 10},
		func(res RelayResult, _ ClaimedEvent, _ error) { results[res]++ })

	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(q.published) != 1 || q.published[0] != ok.ID {
		t.Fatalf("published = %v", q.published)
	}
	if d := q.rescheduled[bad.ID]; d < 4*time.Second || d > 5*time.Second {
		t.Fatalf("backoff after 3 attempts = %v", d)
	}
	if results[RelayPublished] != 1 || results[RelayFailed] != 1 {
		t.Fatalf("results = %v", results)
	}
}

func TestRelayReportsLostLease(t *testing.T) {
	q := &fakeOutbox{batch: []ClaimedEvent{{ID: uuid.New()}}, rescheduled: map[uuid.UUID]time.Duration{}}
	var got RelayResult
	r := NewOutboxRelay(q, fakePublisher{}, RelayConfig{Owner: "a"}, func(res RelayResult, _ ClaimedEvent, _ error) { got = res })
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != RelayLeaseLost {
		t.Fatalf("result = %s", got)
	}
}

func TestRelayBackoffIsCapped(t *testing.T) {
	r := NewOutboxRelay(&fakeOutbox{}, fakePublisher{}, RelayConfig{}, nil)
	for _, attempts := range []int{12, 40, 1 << 20} {
		if d := r.Backoff(attempts); d < 5*time.Minute || d > 6*time.Minute {
			t.Fatalf("Backoff(%d) = %v", attempts, d)
		}
	}
	if d := r.Backoff(1); d < time.Second || d > 1200*time.Millisecond {
		t.Fatalf("Backoff(1) = %v", d)
	}
}
