package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const traceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func remoteContext(t *testing.T) context.Context {
	t.Helper()
	ctx := Extract(context.Background(), map[string]string{"traceparent": traceParent})
	if !trace.SpanContextFromContext(ctx).IsRemote() {
		t.Fatal("traceparent not extracted")
	}
	return ctx
}

func TestPropagationWithoutExporter(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{InstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	ctx, span := otel.Tracer("test").Start(remoteContext(t), "operation")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("spans must not be recorded without an endpoint")
	}
	got := Inject(ctx)["traceparent"]
	if got != traceParent {
		t.Fatalf("forwarded traceparent = %q", got)
	}
	if len(Inject(context.Background())) != 0 {
		t.Fatal("a context without a trace must inject nothing")
	}
}

func TestSetupWithEndpointRecordsSpans(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{InstanceID: "test", Endpoint: "http://127.0.0.1:1", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Setup(context.Background(), Config{}) })
	ctx, span := otel.Tracer("test").Start(remoteContext(t), "operation")
	if !span.IsRecording() || span.SpanContext().TraceID() != trace.SpanContextFromContext(remoteContext(t)).TraceID() {
		t.Fatalf("span = %+v", span.SpanContext())
	}
	if Inject(ctx)["traceparent"] == traceParent {
		t.Fatal("the local span must become the parent of outgoing calls")
	}
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_ = shutdown(ctx)
}

func TestOrphanClientSpansAreDropped(t *testing.T) {
	s := orphanClientSampler{sdktrace.AlwaysSample()}
	cases := []struct {
		name   string
		parent context.Context
		kind   trace.SpanKind
		want   sdktrace.SamplingDecision
	}{
		{"orphan client", context.Background(), trace.SpanKindClient, sdktrace.Drop},
		{"child client", remoteContext(t), trace.SpanKindClient, sdktrace.RecordAndSample},
		{"root server", context.Background(), trace.SpanKindServer, sdktrace.RecordAndSample},
		{"root internal", context.Background(), trace.SpanKindInternal, sdktrace.RecordAndSample},
	}
	for _, c := range cases {
		got := s.ShouldSample(sdktrace.SamplingParameters{ParentContext: c.parent, Kind: c.kind, Name: c.name})
		if got.Decision != c.want {
			t.Errorf("%s: decision = %v", c.name, got.Decision)
		}
	}
}
