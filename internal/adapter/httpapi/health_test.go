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
	var dbErr error
	ready := NewReadiness(func(context.Context) error { return dbErr })
	h := NewAPI(Deps{Log: discardLog, Metrics: metrics.New(), Verifier: fakeVerifier{}, Readiness: ready}).Handler()

	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
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
	dbErr = nil
	ready.StartDraining()
	if c := get("/health/ready"); c != http.StatusServiceUnavailable {
		t.Fatalf("ready while draining = %d", c)
	}
	if c := get("/health/live"); c != http.StatusOK {
		t.Fatalf("live while draining = %d", c)
	}
}
