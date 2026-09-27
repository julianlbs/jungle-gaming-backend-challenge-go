package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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

type contextPublisher struct{ ctx context.Context }

func (p *contextPublisher) Publish(ctx context.Context, _ ClaimedEvent) error {
	p.ctx = ctx
	return nil
}

func TestRelayContinuesStoredTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	ev := ClaimedEvent{ID: uuid.New(), EventType: "WalletBalanceChanged", Attempts: 1,
		TraceParent: "00-" + traceID + "-00f067aa0ba902b7-01"}
	pub := &contextPublisher{}
	q := &fakeOutbox{batch: []ClaimedEvent{ev}, rescheduled: map[uuid.UUID]time.Duration{}, markResult: true}
	if _, err := NewOutboxRelay(q, pub, RelayConfig{Owner: "a"}, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := trace.SpanContextFromContext(pub.ctx).TraceID().String(); got != traceID {
		t.Fatalf("publisher trace = %s", got)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "outbox.publish" || spans[0].SpanKind() != trace.SpanKindProducer ||
		spans[0].Parent().SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("spans = %+v", spans)
	}
}
