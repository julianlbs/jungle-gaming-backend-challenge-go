package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/adapter/auth"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/logging"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/platform/metrics"
)

const (
	headerCorrelationID = "X-Correlation-ID"
	maxBodyBytes        = 64 << 10
)

var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (auth.Principal, error)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// observe assigns the correlation id, recovers panics, and records access logs and latency.
func observe(log *slog.Logger, m *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		corr := r.Header.Get(headerCorrelationID)
		if !correlationPattern.MatchString(corr) {
			corr = uuid.NewString()
		}
		w.Header().Set(headerCorrelationID, corr)
		r = r.WithContext(logging.With(r.Context(), logging.KeyCorrelationID, corr))
		rec := &statusRecorder{ResponseWriter: w}

		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.ErrorContext(r.Context(), "handler panicked", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rec.status == 0 {
					writeProblem(rec, r, problem(http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected error"))
				}
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			span := trace.SpanFromContext(r.Context())
			if strings.HasPrefix(route, r.Method+" ") {
				span.SetName(route)
			} else {
				span.SetName(r.Method + " " + route)
			}
			span.SetAttributes(attribute.String("http.route", route))
			elapsed := time.Since(start)
			m.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Observe(elapsed.Seconds())
			log.InfoContext(r.Context(), "http request",
				"method", r.Method, "route", route, "status", rec.status, "durationMs", elapsed.Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

// authenticated rejects requests without a valid bearer token before any domain access.
func authenticated(v TokenVerifier, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			writeProblem(w, r, problemUnauthenticated)
			return
		}
		p, err := v.Verify(r.Context(), raw)
		if err != nil {
			if !errors.Is(err, auth.ErrInvalidToken) {
				log.WarnContext(r.Context(), "token verification failed", "error", err)
			}
			writeProblem(w, r, problemUnauthenticated)
			return
		}
		ctx := auth.WithPrincipal(r.Context(), p)
		if p.ProviderID != "" {
			ctx = logging.With(ctx, logging.KeyProviderID, p.ProviderID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// requireAnyScope allows the request when the principal holds at least one of the scopes.
func requireAnyScope(next http.HandlerFunc, scopes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		for _, s := range scopes {
			if p.HasScope(s) {
				next(w, r)
				return
			}
		}
		writeProblem(w, r, problemForbidden)
	})
}

func jsonContentType(header string) bool {
	media, _, err := mime.ParseMediaType(header)
	return err == nil && media == "application/json"
}

// decodeJSON reads exactly one JSON object, rejecting unknown fields and oversized bodies.
// The media type must be application/json; a prefix such as application/jsonp is rejected.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) *Problem {
	if !jsonContentType(r.Header.Get("Content-Type")) {
		p := problem(http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "use application/json")
		return &p
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			p := problem(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the body exceeds 64 KiB")
			return &p
		}
		p := validationProblem(FieldProblem{Field: "body", Reason: jsonReason(err)})
		return &p
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		p := validationProblem(FieldProblem{Field: "body", Reason: "must contain a single JSON object"})
		return &p
	}
	return nil
}

func jsonReason(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return "is not valid JSON"
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
	case errors.Is(err, io.EOF):
		return "is required"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "has an unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	default:
		return "is invalid"
	}
}
