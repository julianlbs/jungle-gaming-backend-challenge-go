package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
)

func TestClassify(t *testing.T) {
	pg := func(code, constraint string) error {
		return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code, ConstraintName: constraint})
	}
	cases := []struct {
		name   string
		err    error
		class  errorClass
		reason string
	}{
		{"serialization", pg("40001", ""), classTransient, reasonSerialization},
		{"deadlock", pg("40P01", ""), classTransient, reasonDeadlock},
		{"lock not available", pg("55P03", ""), classTransient, reasonLockTimeout},
		{"statement timeout", pg("57014", ""), classTransient, reasonLockTimeout},
		{"connection failure", pg("08006", ""), classTransient, reasonConnection},
		{"too many connections", pg("53300", ""), classTransient, reasonConnection},
		{"admin shutdown", pg("57P01", ""), classTransient, reasonConnection},
		{"idempotency race", pg("23505", "wager_tx_provider_idempotency_key"), classUniqueRace, reasonUniqueRace},
		{"external id race", pg("23505", "wager_tx_provider_external_key"), classUniqueRace, reasonUniqueRace},
		{"reversal race", pg("23505", "wager_tx_single_reversal"), classUniqueRace, reasonUniqueRace},
		{"inbox redelivery race", pg("23505", "inbox_messages_pkey"), classUniqueRace, reasonUniqueRace},
		{"business unique", pg("23505", "wallets_player_currency_key"), classPermanent, ""},
		{"check violation", pg("23514", "wallets_balance_check"), classPermanent, ""},
		{"undefined column", pg("42703", ""), classPermanent, ""},
		{"canceled", context.Canceled, classPermanent, ""},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), classPermanent, ""},
		{"plain", errors.New("boom"), classPermanent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := classify(tc.err)
			if class != tc.class || reason != tc.reason {
				t.Fatalf("classify = (%v, %q), want (%v, %q)", class, reason, tc.class, tc.reason)
			}
		})
	}
}

func TestUniqueViolation(t *testing.T) {
	err := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505", ConstraintName: "wallets_player_currency_key"})
	if !uniqueViolation(err, "wallets_player_currency_key") {
		t.Fatal("expected match")
	}
	if uniqueViolation(err, "other") {
		t.Fatal("unexpected match on other constraint")
	}
}

func fastPolicy(attempts int) RetryPolicy {
	return RetryPolicy{Attempts: attempts, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func TestRetryRecoversFromTransientFailure(t *testing.T) {
	var reasons []string
	p := fastPolicy(3)
	p.OnRetry = func(r string) { reasons = append(reasons, r) }
	calls := 0
	err := p.do(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "40P01"}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if len(reasons) != 2 || reasons[0] != reasonDeadlock {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestRetryExhaustionIsUnavailable(t *testing.T) {
	calls := 0
	cause := &pgconn.PgError{Code: "55P03"}
	err := fastPolicy(3).do(context.Background(), func(context.Context) error {
		calls++
		return cause
	})
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	if !errors.Is(err, app.ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryStopsOnPermanentError(t *testing.T) {
	calls := 0
	cause := errors.New("invalid")
	err := fastPolicy(5).do(context.Background(), func(context.Context) error {
		calls++
		return cause
	})
	if calls != 1 || !errors.Is(err, cause) || errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRetryHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := RetryPolicy{Attempts: 10, BaseDelay: time.Hour, MaxDelay: time.Hour}
	p.OnRetry = func(string) { cancel() }
	err := p.do(ctx, func(context.Context) error { return &pgconn.PgError{Code: "40001"} })
	if !errors.Is(err, app.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestBackoffBounds(t *testing.T) {
	p := RetryPolicy{BaseDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond}
	for attempt := 1; attempt <= 30; attempt++ {
		for range 50 {
			d := p.backoff(attempt)
			if d <= 0 || d > 40*time.Millisecond {
				t.Fatalf("attempt %d: backoff %v out of bounds", attempt, d)
			}
		}
	}
}

func TestPgInterval(t *testing.T) {
	if got := pgInterval(2 * time.Second); got != "2000ms" {
		t.Fatalf("got %q", got)
	}
	if got := pgInterval(0); got != "0" {
		t.Fatalf("got %q", got)
	}
}
