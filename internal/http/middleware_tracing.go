package tracinghttp

import (
	"context"
	nethttp "net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/myusername/cloudinfer/http"

func ExtractContext(parent context.Context, headers nethttp.Header) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	if headers == nil {
		headers = nethttp.Header{}
	}
	return otel.GetTextMapPropagator().Extract(parent, propagation.HeaderCarrier(headers))
}

func InjectContext(ctx context.Context, headers nethttp.Header) {
	if ctx == nil {
		ctx = context.Background()
	}
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(headers))
}

func NewTracingMiddleware(next nethttp.Handler) nethttp.Handler {
	if next == nil {
		next = nethttp.NotFoundHandler()
	}

	tracer := otel.Tracer(tracerName)
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		ctx := ExtractContext(r.Context(), r.Header)
		ctx, span := tracer.Start(ctx, "http.server", trace.WithSpanKind(trace.SpanKindServer))

		recorder := &statusRecorder{ResponseWriter: w, status: nethttp.StatusOK}
		r = r.WithContext(ctx)
		next.ServeHTTP(recorder, r)

		route := r.Pattern
		if route == "" {
			route = r.URL.Path
		}
		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", route),
			attribute.Int("http.status_code", recorder.status),
		)
		if recorder.status >= 500 {
			span.SetStatus(codes.Error, nethttp.StatusText(recorder.status))
		}
		span.End()
	})
}

type statusRecorder struct {
	nethttp.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}
