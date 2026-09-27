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
	m.ObserveDBFailure("lock_timeout")
	m.ObserveDBFailure("connection")
	m.ObserveWager("SQS", "WIN", "PROCESSED", true, "", false, 20*time.Millisecond)
	m.ObserveWager("HTTP", "BET", "IDEMPOTENCY_CONFLICT", false, "idempotency_key_reused", false, time.Millisecond)
	m.ObserveWager("HTTP", "BET", "CONCURRENT_UPDATE", false, "", true, time.Millisecond)
	m.ObserveReconciliation(false)
	m.ObserveOutboxBacklog(3, 1500*time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`wager_transactions_total{channel="HTTP",kind="BET",status="PROCESSED"} 1`,
		`db_transaction_retries_total{reason="deadlock"} 1`,
		`wallet_concurrency_conflicts_total{reason="lock_timeout"} 1`,
		`wallet_concurrency_conflicts_total{reason="version"} 1`,
		`wager_transactions_total{channel="SQS",kind="WIN",status="PROCESSED"} 1`,
		`wager_idempotent_replays_total{channel="SQS"} 1`,
		`wager_idempotency_conflicts_total{reason="idempotency_key_reused"} 1`,
		`wager_processing_duration_seconds_count{channel="SQS",kind="WIN"} 1`,
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
	if strings.Contains(string(body), `wallet_concurrency_conflicts_total{reason="connection"}`) {
		t.Error("connection failures are not concurrency conflicts")
	}
}
