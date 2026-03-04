package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	adapteropenai "github.com/myusername/cloudinfer/internal/adapters/openai"
	"github.com/myusername/cloudinfer/internal/backends/wrap"
	"github.com/myusername/cloudinfer/internal/config"
	tracinghttp "github.com/myusername/cloudinfer/internal/http"
	"github.com/myusername/cloudinfer/internal/metrics"
	otelsetup "github.com/myusername/cloudinfer/internal/otel"
	"github.com/myusername/cloudinfer/internal/routing"
	otelglobal "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTracingPreservesIncomingTraceAcrossServerRouterAndAdapter(t *testing.T) {
	upstreamTraceparent := make(chan string, 1)
	upstreamTracestate := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case upstreamTraceparent <- r.Header.Get("Traceparent"):
		default:
		}
		select {
		case upstreamTracestate <- r.Header.Get("Tracestate"):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"status\":\"completed\"}\n\n")
	}))
	defer upstream.Close()

	t.Setenv("TEST_OPENAI_API_KEY", "test-key")

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prevProvider := otelglobal.GetTracerProvider()
	prevPropagator := otelglobal.GetTextMapPropagator()
	t.Cleanup(func() {
		otelglobal.SetTracerProvider(prevProvider)
		otelglobal.SetTextMapPropagator(prevPropagator)
		_ = provider.Shutdown(context.Background())
	})
	otelsetup.InstallGlobals(provider)

	adapter, err := adapteropenai.NewWithClient(config.OpenAIConfig{
		APIKeyEnv: "TEST_OPENAI_API_KEY",
		BaseURL:   upstream.URL,
		Model:     "gpt-4o-mini",
	}, upstream.Client())
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}

	router := routing.NewRouter(
		[]routing.Backend{
			{
				Name:   "alpha",
				Type:   "openai",
				Client: wrap.NewAdapterStreamer("alpha", "gpt-4o-mini", adapter),
			},
		},
		routing.NewStatsStore(0.2, 15*time.Second),
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1},
	)

	cfg := &config.Config{}
	runtime := readyRuntime()
	mux := http.NewServeMux()
	NewServer(cfg, noopLogger{}, metrics.New(), router, runtime, nil).RegisterRoutes(mux)
	server := httptest.NewServer(tracinghttp.NewTracingMiddleware(mux))
	defer server.Close()

	traceIDHex := "4bf92f3577b34da6a3ce929d0e0e4736"
	spanIDHex := "00f067aa0ba902b7"
	prompt := "top-secret-user-prompt"
	reqBody := strings.NewReader(`{"model":"default","stream":false,"input":[{"role":"user","content":"` + prompt + `"}]}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", reqBody)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Traceparent", "00-"+traceIDHex+"-"+spanIDHex+"-01")
	req.Header.Set("Tracestate", "vendor=value")

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatalf("force flush spans: %v", err)
	}

	select {
	case got := <-upstreamTraceparent:
		if got == "" {
			t.Fatal("upstream request is missing traceparent")
		}
		if !strings.Contains(got, traceIDHex) {
			t.Fatalf("upstream traceparent = %q, want trace id %q", got, traceIDHex)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream traceparent")
	}
	select {
	case got := <-upstreamTracestate:
		if got != "vendor=value" {
			t.Fatalf("upstream tracestate = %q, want %q", got, "vendor=value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream tracestate")
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans, got none")
	}

	traceID, err := trace.TraceIDFromHex(traceIDHex)
	if err != nil {
		t.Fatalf("parse trace id: %v", err)
	}

	names := make([]string, 0, len(spans))
	var sawRouterSelect bool
	for _, span := range spans {
		names = append(names, span.Name)
		if span.SpanContext.TraceID() != traceID {
			t.Fatalf("span %q trace id = %s, want %s", span.Name, span.SpanContext.TraceID(), traceID)
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), prompt) {
				t.Fatalf("span %q leaked request payload content", span.Name)
			}
		}
		for _, event := range span.Events {
			if strings.Contains(event.Name, prompt) {
				t.Fatalf("span %q event %q leaked request payload content", span.Name, event.Name)
			}
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.AsString(), prompt) {
					t.Fatalf("span %q event leaked request payload content", span.Name)
				}
			}
		}
		if span.Name == "router.select" {
			sawRouterSelect = true
			if got := attributeValue(span.Attributes, "backend"); got != "alpha" {
				t.Fatalf("router.select backend = %q, want %q", got, "alpha")
			}
			if got := attributeValue(span.Attributes, "reason"); got == "" {
				t.Fatal("router.select reason attribute is empty")
			}
		}
	}

	for _, want := range []string{"http.server", "router.select", "adapter.call"} {
		if !slices.Contains(names, want) {
			t.Fatalf("span names = %v, missing %q", names, want)
		}
	}
	if !sawRouterSelect {
		t.Fatal("missing router.select span")
	}
}

func attributeValue(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}
