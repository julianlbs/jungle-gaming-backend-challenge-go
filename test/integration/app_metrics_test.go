//go:build integration

package integration

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

func TestWagerOutcomesAreExposedAsMetrics(t *testing.T) {
	f := newAppFixture(t)
	m := metrics.New()
	f.processor.WithObserver(func(o app.WagerObservation) {
		m.ObserveWager(o.Channel, o.Kind, o.Status, o.Replay, o.Conflict, o.ConcurrencyConflict, o.Duration)
	})
	ctx := context.Background()
	w := f.openWallet(t, "100.00")

	cmd := wager(w, "BET", "30.00", "metrics-bet")
	for range 2 {
		if _, err := f.processor.Process(ctx, cmd); err != nil {
			t.Fatal(err)
		}
	}
	changed := cmd
	changed.Amount = "31.00"
	_, _ = f.processor.Process(ctx, changed)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`wager_transactions_total{channel="HTTP",kind="BET",status="PROCESSED"} 2`,
		`wager_transactions_total{channel="HTTP",kind="BET",status="IDEMPOTENCY_CONFLICT"} 1`,
		`wager_idempotent_replays_total{channel="HTTP"} 1`,
		`wager_idempotency_conflicts_total{reason="idempotency_key_reused"} 1`,
		`wager_processing_duration_seconds_count{channel="HTTP",kind="BET"} 3`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("exposition misses %q", want)
		}
	}
}
