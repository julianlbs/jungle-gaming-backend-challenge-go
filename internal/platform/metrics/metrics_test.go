package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExposition(t *testing.T) {
	m := New()
	m.WagerTransactions.WithLabelValues("HTTP", "BET", "PROCESSED").Inc()
	m.ObserveDBRetry("deadlock")
	m.ObserveReconciliation(false)
	m.ObserveOutboxBacklog(3, 1500*time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`wager_transactions_total{channel="HTTP",kind="BET",status="PROCESSED"} 1`,
		`db_transaction_retries_total{reason="deadlock"} 1`,
		`wallet_reconciliation_runs_total{consistent="false"} 1`,
		`wallet_reconciliation_divergences_total 1`,
		`outbox_pending_events 3`,
		`outbox_oldest_pending_age_seconds 1.5`,
		`go_goroutines`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("exposition misses %q", want)
		}
	}
}
