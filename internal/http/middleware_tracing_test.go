package tracinghttp

import (
	"context"
	nethttp "net/http"
	"testing"

	otelsetup "github.com/myusername/cloudinfer/internal/otel"
	otelglobal "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func TestExtractAndInjectPreserveTraceparent(t *testing.T) {
	prevProvider := otelglobal.GetTracerProvider()
	prevPropagator := otelglobal.GetTextMapPropagator()
	t.Cleanup(func() {
		otelglobal.SetTracerProvider(prevProvider)
		otelglobal.SetTextMapPropagator(prevPropagator)
	})
	otelsetup.InstallGlobals(trace.NewNoopTracerProvider())

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("parse trace id: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("parse span id: %v", err)
	}

	inboundCtx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))

	headers := nethttp.Header{}
	InjectContext(inboundCtx, headers)
	if got := headers.Get("Traceparent"); got == "" {
		t.Fatal("traceparent header was not injected")
	}

	extracted := ExtractContext(context.Background(), headers)
	got := trace.SpanContextFromContext(extracted)
	if !got.IsValid() {
		t.Fatal("extracted span context is invalid")
	}
	if got.TraceID() != traceID {
		t.Fatalf("trace id = %s, want %s", got.TraceID(), traceID)
	}
	if got.SpanID() != spanID {
		t.Fatalf("span id = %s, want %s", got.SpanID(), spanID)
	}

	roundTrip := nethttp.Header{}
	InjectContext(extracted, roundTrip)
	if got := roundTrip.Get("Traceparent"); got != headers.Get("Traceparent") {
		t.Fatalf("traceparent = %q, want %q", got, headers.Get("Traceparent"))
	}
}
