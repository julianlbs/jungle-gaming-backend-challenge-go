// Package tracing configures OpenTelemetry spans and W3C trace context propagation.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const ServiceName = "jungle-wallet"

type Config struct {
	InstanceID string
	// Endpoint is the OTLP/HTTP base URL; empty disables span export.
	Endpoint    string
	SampleRatio float64
}

// Setup installs the global propagator and tracer provider. Without an endpoint spans are not
// recorded, but trace context received over HTTP or SQS is still forwarded to SNS.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if cfg.Endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.Endpoint+"/v1/traces"))
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(orphanClientSampler{sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))}),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", ServiceName),
			attribute.String("service.instance.id", cfg.InstanceID),
		)),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// orphanClientSampler drops client spans without a parent, so that database polls made by the
// workers outside any operation do not become traces of their own.
type orphanClientSampler struct {
	sdktrace.Sampler
}

func (s orphanClientSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	parent := trace.SpanContextFromContext(p.ParentContext)
	if p.Kind == trace.SpanKindClient && !parent.IsValid() {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: parent.TraceState()}
	}
	return s.Sampler.ShouldSample(p)
}

func (s orphanClientSampler) Description() string {
	return "OrphanClientDrop{" + s.Sampler.Description() + "}"
}

// Inject returns the trace context of ctx as string key/value pairs.
func Inject(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

// Extract returns ctx with the remote trace context found in carrier, if any.
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}
