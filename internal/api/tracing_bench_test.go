package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func BenchmarkTracingRequestOverhead(b *testing.B) {
	b.Run("off", func(b *testing.B) {
		handler, cleanup := newTracingBenchmarkHandler(b, false)
		defer cleanup()
		benchmarkResponsesRequest(b, handler)
	})

	b.Run("on_sampled", func(b *testing.B) {
		handler, cleanup := newTracingBenchmarkHandler(b, true)
		defer cleanup()
		benchmarkResponsesRequest(b, handler)
	})
}

func newTracingBenchmarkHandler(tb testing.TB, traced bool) (http.Handler, func()) {
	tb.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output_text":"hello"}`)
	}))

	prevProvider := otelglobal.GetTracerProvider()
	prevPropagator := otelglobal.GetTextMapPropagator()
	restoreGlobals := func() {
		otelglobal.SetTracerProvider(prevProvider)
		otelglobal.SetTextMapPropagator(prevPropagator)
	}

	shutdownTracing := func() {}
	if traced {
		provider := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithSyncer(discardExporter{}),
		)
		otelsetup.InstallGlobals(provider)
		shutdownTracing = func() {
			_ = provider.Shutdown(context.Background())
		}
	} else {
		otelsetup.InstallGlobals(oteltrace.NewNoopTracerProvider())
	}

	tb.Setenv("BENCH_OPENAI_API_KEY", "bench-key")
	adapter, err := adapteropenai.NewWithClient(config.OpenAIConfig{
		APIKeyEnv: "BENCH_OPENAI_API_KEY",
		BaseURL:   upstream.URL,
		Model:     "gpt-4o-mini",
	}, upstream.Client())
	if err != nil {
		upstream.Close()
		restoreGlobals()
		tb.Fatalf("create benchmark adapter: %v", err)
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

	mux := http.NewServeMux()
	NewServer(&config.Config{}, noopLogger{}, metrics.New(), router, readyRuntime(), nil).RegisterRoutes(mux)
	handler := http.Handler(mux)
	if traced {
		handler = tracinghttp.NewTracingMiddleware(mux)
	}

	return handler, func() {
		upstream.Close()
		shutdownTracing()
		restoreGlobals()
	}
}

func benchmarkResponsesRequest(b *testing.B, handler http.Handler) {
	b.Helper()

	body := `{"model":"default","stream":false,"input":[{"role":"user","content":"hello"}]}`
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
		}
	}
}

type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }
