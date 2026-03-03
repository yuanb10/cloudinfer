package metrics

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const MetricsContentType = "text/plain; version=0.0.4"

type Options struct {
	DevMode bool
}

type Collector struct {
	requestsTotal      *prometheus.CounterVec
	ttftSeconds        *prometheus.HistogramVec
	streamDuration     *prometheus.HistogramVec
	draining           prometheus.Gauge
	registry           *prometheus.Registry
	registrationErrors []error
	devMode            bool
}

func New() *Collector {
	return NewWithOptions(Options{DevMode: defaultDevMode()})
}

func NewWithOptions(opts Options) *Collector {
	c := &Collector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cloudinfer_requests_total",
				Help: "Total number of handled requests by endpoint, backend, and terminal status.",
			},
			[]string{"endpoint", "backend", "status"},
		),
		ttftSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cloudinfer_ttft_seconds",
				Help:    "Time to first token in seconds.",
				Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
			[]string{"backend", "model"},
		),
		streamDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cloudinfer_stream_duration_seconds",
				Help:    "Streaming response duration in seconds.",
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"backend", "model"},
		),
		draining: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "cloudinfer_draining",
				Help: "Whether the server is draining new work (1) or accepting it (0).",
			},
		),
		registry: prometheus.NewRegistry(),
		devMode:  opts.DevMode,
	}

	c.mustRegister(c.requestsTotal)
	c.mustRegister(c.ttftSeconds)
	c.mustRegister(c.streamDuration)
	c.mustRegister(c.draining)
	c.draining.Set(0)

	return c
}

func defaultDevMode() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("CLOUDINFER_ENV")))
	return value != "prod" && value != "production"
}

func (c *Collector) mustRegister(collector prometheus.Collector) {
	if err := c.registry.Register(collector); err != nil {
		c.registrationErrors = append(c.registrationErrors, err)
	}
}

func (c *Collector) ObserveChatCompletion(endpoint string, backend string, model string, status string, ttft time.Duration, total time.Duration, stream bool) {
	if c == nil {
		return
	}

	reqLabels := requestLabels{
		Endpoint: normalizeLabel(endpoint, "unknown"),
		Backend:  normalizeLabel(backend, "unknown"),
		Status:   normalizeLabel(status, "unknown"),
	}
	latencyLabels := latencyLabels{
		Backend: normalizeLabel(backend, "unknown"),
		Model:   normalizeLabel(model, "unknown"),
	}

	c.requestsTotal.WithLabelValues(reqLabels.Endpoint, reqLabels.Backend, reqLabels.Status).Inc()

	if ttft >= 0 {
		c.ttftSeconds.WithLabelValues(latencyLabels.Backend, latencyLabels.Model).Observe(ttft.Seconds())
	}
	if stream && total > 0 {
		c.streamDuration.WithLabelValues(latencyLabels.Backend, latencyLabels.Model).Observe(total.Seconds())
	}
}

func (c *Collector) SetDraining(draining bool) {
	if c == nil {
		return
	}

	if draining {
		c.draining.Set(1)
		return
	}
	c.draining.Set(0)
}

func (c *Collector) Gather() ([]*dto.MetricFamily, error) {
	if c == nil {
		return nil, nil
	}

	families, err := c.registry.Gather()
	if err != nil {
		return nil, err
	}
	if c.devMode {
		if err := ValidateMetricFamilies(families); err != nil {
			return nil, err
		}
	}

	return families, nil
}

func (c *Collector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		families, err := c.Gather()
		if err != nil {
			http.Error(w, "metrics unavailable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if len(c.registrationErrors) > 0 {
			http.Error(w, "metrics unavailable: "+errors.Join(c.registrationErrors...).Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", MetricsContentType)
		encoder := expfmt.NewEncoder(w, expfmt.FmtText)
		for _, family := range families {
			if err := encoder.Encode(family); err != nil {
				http.Error(w, "metrics encode failed", http.StatusInternalServerError)
				return
			}
		}
	})
}

func (c *Collector) Register(mux *http.ServeMux) {
	mux.Handle("GET /metrics", c.Handler())
}

func normalizeLabel(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}
