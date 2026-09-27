package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
)

// Problem is an RFC 9457 problem details body.
type Problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Code          string         `json:"code"`
	Retryable     bool           `json:"retryable"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Errors        []FieldProblem `json:"errors,omitempty"`
}

type FieldProblem struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func problem(status int, code, detail string) Problem {
	return Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

var (
	problemUnauthenticated = problem(http.StatusUnauthorized, "UNAUTHENTICATED", "a valid bearer token is required")
	problemForbidden       = problem(http.StatusForbidden, "FORBIDDEN", "the token lacks the required scope")
	problemNotFound        = problem(http.StatusNotFound, "NOT_FOUND", "resource not found")
)

func validationProblem(fields ...FieldProblem) Problem {
	p := problem(http.StatusBadRequest, "VALIDATION_ERROR", "the request is invalid")
	p.Errors = fields
	return p
}

// problemFor maps use case errors to responses. Unknown errors are internal and their details
// are logged, never returned.
func problemFor(err error) (Problem, bool) {
	var fe *wagering.FieldError
	switch {
	case errors.As(err, &fe):
		return validationProblem(FieldProblem{Field: fe.Field, Reason: fe.Reason}), true
	case errors.Is(err, wagering.ErrReservedKind):
		return validationProblem(FieldProblem{Field: "kind", Reason: "is reserved for internal use"}), true
	case errors.Is(err, app.ErrWalletNotFound):
		return problem(http.StatusBadRequest, "WALLET_NOT_FOUND", "the wallet does not exist"), true
	case errors.Is(err, app.ErrWalletMismatch):
		return problem(http.StatusBadRequest, "WALLET_MISMATCH", "the wallet does not match the player or currency"), true
	case errors.Is(err, app.ErrNotFound):
		return problemNotFound, true
	case errors.Is(err, app.ErrWalletAlreadyExists):
		return problem(http.StatusConflict, "WALLET_ALREADY_EXISTS", "the player already has a wallet in this currency"), true
	case errors.Is(err, app.ErrIdempotencyKeyReused):
		return problem(http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "the idempotency key was used with a different payload"), true
	case errors.Is(err, app.ErrExternalTransactionConflict):
		return problem(http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT", "the external transaction id was used with another idempotency key"), true
	case errors.Is(err, app.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		p := problem(http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "temporarily unavailable, retry with the same idempotency key")
		p.Retryable = true
		return p, true
	default:
		return problem(http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected error"), false
	}
}

func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.CorrelationID = logging.CorrelationID(r.Context())
	switch p.Status {
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	case http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", "1")
	}
	writeJSONStatus(w, "application/problem+json", p.Status, p)
}

// writeError renders err, logging it when it is not an expected outcome.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	p, known := problemFor(err)
	if !known {
		log.ErrorContext(r.Context(), "request failed", "error", err)
	} else if p.Status == http.StatusServiceUnavailable {
		log.WarnContext(r.Context(), "request unavailable", "error", err)
	}
	writeProblem(w, r, p)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	writeJSONStatus(w, "application/json", status, body)
}

func writeJSONStatus(w http.ResponseWriter, contentType string, status int, body any) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
