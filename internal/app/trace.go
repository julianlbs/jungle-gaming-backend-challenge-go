package app

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app")

// annotateWagerSpan copies the bounded observation labels onto the span. Amounts and
// identifiers must not become span attributes.
func annotateWagerSpan(span trace.Span, o WagerObservation, err error) {
	span.SetAttributes(
		attribute.String("wager.channel", o.Channel),
		attribute.String("wager.kind", o.Kind),
		attribute.String("wager.status", o.Status),
		attribute.Bool("wager.replay", o.Replay),
	)
	switch o.Status {
	case ResultError, ResultUnavailable, ResultConcurrent:
		span.RecordError(err)
		span.SetStatus(codes.Error, o.Status)
	}
}

func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed")
	}
	span.End()
}
