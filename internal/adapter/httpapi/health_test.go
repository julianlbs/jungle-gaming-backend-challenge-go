package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

func TestHealth(t *testing.T) {
	var dbErr, sqsErr error
	ready := NewReadiness(
		ReadinessCheck{Name: "database", Check: func(context.Context) error { return dbErr }},
		ReadinessCheck{Name: "sqs", Check: func(context.Context) error { return sqsErr }},
	)
	h := NewAPI(Deps{Log: discardLog, Metrics: metrics.New(), Verifier: fakeVerifier{}, Readiness: ready}).Handler()

	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	status := func() string {
		s, _ := ready.Ready(context.Background())
		return s
	}

	if c := get("/health/live"); c != http.StatusOK {
		t.Fatalf("live = %d", c)
	}
	if c := get("/health/ready"); c != http.StatusOK {
		t.Fatalf("ready = %d", c)
	}
	dbErr = errors.New("down")
	if c := get("/health/ready"); c != http.StatusServiceUnavailable {
		t.Fatalf("ready with db down = %d", c)
	}
	if s := status(); s != "database unavailable" {
		t.Fatalf("status = %q", s)
	}
	dbErr, sqsErr = nil, errors.New("down")
	if c := get("/health/ready"); c != http.StatusServiceUnavailable || status() != "sqs unavailable" {
		t.Fatalf("ready with sqs down = %d %q", c, status())
	}
	sqsErr = nil
	ready.StartDraining()
	if c := get("/health/ready"); c != http.StatusServiceUnavailable {
		t.Fatalf("ready while draining = %d", c)
	}
	if c := get("/health/live"); c != http.StatusOK {
		t.Fatalf("live while draining = %d", c)
	}
}
