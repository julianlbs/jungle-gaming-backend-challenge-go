package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeVerifier map[string]auth.Principal

func (f fakeVerifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	if p, ok := f[raw]; ok {
		return p, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q body %s", ct, rec.Body)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuthenticationAndScopes(t *testing.T) {
	a := NewAPI(Deps{Log: discardLog, Metrics: metrics.New(), Verifier: fakeVerifier{
		"reader": {ClientID: "r", Scopes: []string{auth.ScopeWalletsRead}},
		"other":  {ClientID: "o", Scopes: []string{auth.ScopeWageringRead}},
	}})
	a.protected("GET /thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, auth.ScopeWalletsRead)
	h := a.Handler()

	for name, tc := range map[string]struct {
		header string
		status int
		code   string
	}{
		"missing token":  {"", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"wrong scheme":   {"Basic abc", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"invalid token":  {"Bearer nope", http.StatusUnauthorized, "UNAUTHENTICATED"},
		"missing scope":  {"Bearer other", http.StatusForbidden, "FORBIDDEN"},
		"allowed":        {"Bearer reader", http.StatusNoContent, ""},
		"case of scheme": {"bearer reader", http.StatusNoContent, ""},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/thing", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d", rec.Code)
			}
			if tc.code == "" {
				return
			}
			if p := decodeProblem(t, rec); p.Code != tc.code || p.CorrelationID == "" {
				t.Fatalf("problem = %+v", p)
			}
			if tc.status == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate")
			}
		})
	}
}

func TestCorrelationIDAndPanicRecovery(t *testing.T) {
	a := NewAPI(Deps{Log: discardLog, Metrics: metrics.New(), Verifier: fakeVerifier{}})
	a.public("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := a.Handler()

	req := httptest.NewRequest("GET", "/boom", nil)
	req.Header.Set(headerCorrelationID, "client-corr-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || rec.Header().Get(headerCorrelationID) != "client-corr-1" {
		t.Fatalf("status %d corr %q", rec.Code, rec.Header().Get(headerCorrelationID))
	}
	if p := decodeProblem(t, rec); p.CorrelationID != "client-corr-1" || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("problem leaks or lacks correlation: %s", rec.Body)
	}

	req = httptest.NewRequest("GET", "/missing", nil)
	req.Header.Set(headerCorrelationID, "bad id with spaces")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || rec.Header().Get(headerCorrelationID) == "bad id with spaces" {
		t.Fatalf("status %d corr %q", rec.Code, rec.Header().Get(headerCorrelationID))
	}
}

func TestProblemMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&wagering.FieldError{Field: "money.amount", Reason: "bad"}, 400, "VALIDATION_ERROR"},
		{wagering.ErrReservedKind, 400, "VALIDATION_ERROR"},
		{app.ErrWalletNotFound, 400, "WALLET_NOT_FOUND"},
		{app.ErrWalletMismatch, 400, "WALLET_MISMATCH"},
		{app.ErrNotFound, 404, "NOT_FOUND"},
		{app.ErrWalletAlreadyExists, 409, "WALLET_ALREADY_EXISTS"},
		{app.ErrIdempotencyKeyReused, 409, "IDEMPOTENCY_KEY_REUSED"},
		{app.ErrExternalTransactionConflict, 409, "EXTERNAL_TRANSACTION_CONFLICT"},
		{fmt.Errorf("x: %w", app.ErrUnavailable), 503, "SERVICE_UNAVAILABLE"},
		{errors.New("secret detail"), 500, "INTERNAL_ERROR"},
	} {
		p, _ := problemFor(tc.err)
		if p.Status != tc.status || p.Code != tc.code {
			t.Errorf("%v -> %d %s", tc.err, p.Status, p.Code)
		}
		if strings.Contains(p.Detail, "secret") {
			t.Errorf("internal detail leaked: %s", p.Detail)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	for name, tc := range map[string]struct {
		body, contentType string
		status            int
	}{
		"valid":         {`{"name":"a"}`, "application/json", 0},
		"unknown field": {`{"name":"a","x":1}`, "application/json", 400},
		"two objects":   {`{"name":"a"}{"name":"b"}`, "application/json", 400},
		"not json":      {`{`, "application/json", 400},
		"empty":         {``, "application/json", 400},
		"wrong type":    {`{"name":1}`, "application/json", 400},
		"media type":    {`{"name":"a"}`, "text/plain", 415},
		"too large":     {`{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, "application/json", 413},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			var b body
			p := decodeJSON(httptest.NewRecorder(), req, &b)
			if tc.status == 0 {
				if p != nil || b.Name != "a" {
					t.Fatalf("problem %+v name %q", p, b.Name)
				}
				return
			}
			if p == nil || p.Status != tc.status {
				t.Fatalf("problem = %+v", p)
			}
		})
	}
}
