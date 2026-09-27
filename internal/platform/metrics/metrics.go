// Package metrics defines the Prometheus instruments exposed on the metrics port.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry *prometheus.Registry

	WagerTransactions       *prometheus.CounterVec
	IdempotentReplays       *prometheus.CounterVec
	IdempotencyConflicts    *prometheus.CounterVec
	WagerDuration           *prometheus.HistogramVec
	InboxDuplicates         *prometheus.CounterVec
	DBRetries               *prometheus.CounterVec
	ConcurrencyConflicts    *prometheus.CounterVec
	SQSMessages             *prometheus.CounterVec
	SQSRetries              prometheus.Counter
	SQSDeadLettered         *prometheus.CounterVec
	OutboxPending           prometheus.Gauge
	OutboxOldestAge         prometheus.Gauge
	OutboxPublish           *prometheus.CounterVec
	PendingReferences       prometheus.Gauge
	ReferenceResolutions    *prometheus.CounterVec
	ReconciliationRuns      *prometheus.CounterVec
	ReconciliationDivergent prometheus.Counter
	HTTPRequests            *prometheus.HistogramVec
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := factory{reg}
	return &Metrics{
		registry: reg,
		WagerTransactions: f.counterVec("wager_transactions_total",
			"Wager operations by resulting status.", "channel", "kind", "status"),
		IdempotentReplays: f.counterVec("wager_idempotent_replays_total",
			"Requests answered from a previously recorded operation.", "channel"),
		IdempotencyConflicts: f.counterVec("wager_idempotency_conflicts_total",
			"Requests colliding with a different recorded operation.", "reason"),
		WagerDuration: f.histogramVec("wager_processing_duration_seconds",
			"Time to process a wager operation.", []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}, "channel", "kind"),
		InboxDuplicates: f.counterVec("inbox_duplicates_total",
			"Broker messages already handled by the consumer.", "consumer"),
		DBRetries: f.counterVec("db_transaction_retries_total",
			"Database transactions retried after a transient failure.", "reason"),
		ConcurrencyConflicts: f.counterVec("wallet_concurrency_conflicts_total",
			"Transactions that lost to a concurrent writer: lock timeout, serialization, deadlock or version check.", "reason"),
		SQSMessages: f.counterVec("sqs_messages_total",
			"Consumed messages by outcome.", "outcome"),
		SQSRetries: f.counter("sqs_message_retries_total",
			"Messages returned to the queue for another attempt."),
		SQSDeadLettered: f.counterVec("sqs_dead_lettered_total",
			"Messages moved to the dead-letter queue.", "reason"),
		OutboxPending: f.gauge("outbox_pending_events",
			"Outbox events not yet published."),
		OutboxOldestAge: f.gauge("outbox_oldest_pending_age_seconds",
			"Age of the oldest unpublished outbox event."),
		OutboxPublish: f.counterVec("outbox_publish_total",
			"Outbox publication attempts by result.", "result"),
		PendingReferences: f.gauge("pending_reference_transactions",
			"Operations waiting for their reference."),
		ReferenceResolutions: f.counterVec("reference_resolutions_total",
			"Pending reference resolution attempts by result.", "result"),
		ReconciliationRuns: f.counterVec("wallet_reconciliation_runs_total",
			"Wallet reconciliations by outcome.", "consistent"),
		ReconciliationDivergent: f.counter("wallet_reconciliation_divergences_total",
			"Reconciliations where the balance did not match the ledger."),
		HTTPRequests: f.histogramVec("http_request_duration_seconds",
			"HTTP request latency.", prometheus.DefBuckets, "method", "route", "status"),
	}
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// ObserveDBRetry is compatible with postgres.RetryPolicy.OnRetry.
func (m *Metrics) ObserveDBRetry(reason string) {
	m.DBRetries.WithLabelValues(reason).Inc()
}

// ObserveDBFailure is compatible with postgres.RetryPolicy.OnFailure.
func (m *Metrics) ObserveDBFailure(reason string) {
	switch reason {
	case "lock_timeout", "serialization", "deadlock":
		m.ConcurrencyConflicts.WithLabelValues(reason).Inc()
	}
}

// ObserveWager records one handled wager. Label values must come from bounded sets.
func (m *Metrics) ObserveWager(channel, kind, status string, replay bool, conflict string, concurrency bool, d time.Duration) {
	m.WagerTransactions.WithLabelValues(channel, kind, status).Inc()
	m.WagerDuration.WithLabelValues(channel, kind).Observe(d.Seconds())
	if replay {
		m.IdempotentReplays.WithLabelValues(channel).Inc()
	}
	if conflict != "" {
		m.IdempotencyConflicts.WithLabelValues(conflict).Inc()
	}
	if concurrency {
		m.ConcurrencyConflicts.WithLabelValues("version").Inc()
	}
}

func (m *Metrics) ObserveReconciliation(consistent bool) {
	m.ReconciliationRuns.WithLabelValues(strconv.FormatBool(consistent)).Inc()
	if !consistent {
		m.ReconciliationDivergent.Inc()
	}
}

func (m *Metrics) ObserveOutboxBacklog(count int64, oldest time.Duration) {
	m.OutboxPending.Set(float64(count))
	m.OutboxOldestAge.Set(oldest.Seconds())
}

type factory struct {
	reg *prometheus.Registry
}

func (f factory) counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	f.reg.MustRegister(c)
	return c
}

func (f factory) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.reg.MustRegister(g)
	return g
}

func (f factory) histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	f.reg.MustRegister(h)
	return h
}
