// Package logging builds the structured logger and carries request-scoped fields in contexts.
package logging

import (
	"context"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type ctxKey struct{}

var timeZero time.Time

// Field names shared by every log line that carries them.
const (
	KeyCorrelationID = "correlationId"
	KeyMessageID     = "messageId"
	KeyTransactionID = "transactionId"
	KeyWalletID      = "walletId"
	KeyProviderID    = "providerId"
	KeyInstanceID    = "instanceId"
	KeyTraceID       = "traceId"
)

// New returns a JSON logger that also emits the attributes stored in each record's context.
func New(w io.Writer, level slog.Level, instanceID string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(contextHandler{Handler: h}).With(KeyInstanceID, instanceID)
}

// With returns a context whose log lines include the given key/value pairs.
func With(ctx context.Context, args ...any) context.Context {
	existing, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	attrs := make([]slog.Attr, len(existing), len(existing)+len(args)/2)
	copy(attrs, existing)
	r := slog.NewRecord(timeZero, 0, "", 0)
	r.Add(args...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	return context.WithValue(ctx, ctxKey{}, attrs)
}

// CorrelationID returns the correlation id stored with With, if any.
func CorrelationID(ctx context.Context) string {
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	for i := len(attrs) - 1; i >= 0; i-- {
		if attrs[i].Key == KeyCorrelationID {
			return attrs[i].Value.String()
		}
	}
	return ""
}

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String(KeyTraceID, sc.TraceID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}
