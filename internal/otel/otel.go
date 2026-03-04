package otel

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	defaultServiceName = "cloudinfer"
)

func Bootstrap(ctx context.Context) (func(context.Context) error, error) {
	endpoint := strings.TrimSpace(firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	))
	if endpoint == "" {
		InstallGlobals(oteltrace.NewNoopTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	clientOpts, err := exporterOptions(endpoint)
	if err != nil {
		return nil, err
	}

	exporter, err := otlptracehttp.New(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", serviceName()),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFromEnv()),
	)
	InstallGlobals(provider)

	return func(ctx context.Context) error {
		return provider.Shutdown(ctx)
	}, nil
}

func InstallGlobals(provider oteltrace.TracerProvider) {
	if provider == nil {
		provider = oteltrace.NewNoopTracerProvider()
	}
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(defaultPropagator())
}

func defaultPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func exporterOptions(raw string) ([]otlptracehttp.Option, error) {
	opts := make([]otlptracehttp.Option, 0, 4)

	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		opts = append(opts, otlptracehttp.WithEndpointURL(raw))
		if strings.EqualFold(parsed.Scheme, "http") {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	} else {
		opts = append(opts, otlptracehttp.WithEndpoint(raw))
		if envBool("OTEL_EXPORTER_OTLP_TRACES_INSECURE") || envBool("OTEL_EXPORTER_OTLP_INSECURE") {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	}

	if headers := parseHeaders(firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"),
		os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
	)); len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}

	if timeout := parseDurationMillis(firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT"),
		os.Getenv("OTEL_EXPORTER_OTLP_TIMEOUT"),
	)); timeout > 0 {
		opts = append(opts, otlptracehttp.WithTimeout(timeout))
	}

	return opts, nil
}

func samplerFromEnv() sdktrace.Sampler {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER"))) {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "traceidratio":
		ratio := 1.0
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")), 64); err == nil {
			ratio = parsed
		}
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		return sdktrace.TraceIDRatioBased(ratio)
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

func parseHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	headers := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		headers[key] = value
	}
	if len(headers) == 0 {
		return nil
	}

	return headers
}

func parseDurationMillis(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func serviceName() string {
	return strings.TrimSpace(firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), defaultServiceName))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
