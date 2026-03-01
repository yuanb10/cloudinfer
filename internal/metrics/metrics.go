package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Collector struct {
	requestsTotal      *prometheus.CounterVec
	ttftMs             *prometheus.HistogramVec
	totalLatencyMs     *prometheus.HistogramVec
	registry           *prometheus.Registry
	registrationErrors []error
}

func New() *Collector {
	c := &Collector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cloudinfer_requests_total",
				Help: "Total number of chat completion requests by terminal status.",
			},
			[]string{"status", "stream"},
		),
		ttftMs: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cloudinfer_ttft_ms",
				Help:    "Time to first token in milliseconds.",
				Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
			},
			[]string{"stream"},
		),
		totalLatencyMs: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cloudinfer_total_latency_ms",
				Help:    "Total request latency in milliseconds.",
				Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
			},
			[]string{"stream"},
		),
		registry: prometheus.NewRegistry(),
	}

	c.mustRegister(c.requestsTotal)
	c.mustRegister(c.ttftMs)
	c.mustRegister(c.totalLatencyMs)

	return c
}

func (c *Collector) mustRegister(collector prometheus.Collector) {
	if err := c.registry.Register(collector); err != nil {
		c.registrationErrors = append(c.registrationErrors, err)
	}
}

func (c *Collector) Observe(status string, stream bool, ttftMs int64, totalLatencyMs int64) {
	streamLabel := strconv.FormatBool(stream)

	c.requestsTotal.WithLabelValues(status, streamLabel).Inc()
	c.totalLatencyMs.WithLabelValues(streamLabel).Observe(float64(totalLatencyMs))

	if ttftMs > 0 {
		c.ttftMs.WithLabelValues(streamLabel).Observe(float64(ttftMs))
	}
}

func (c *Collector) Handler() http.Handler {
	return promhttp.HandlerFor(c.registry, promhttp.HandlerOpts{})
}

func (c *Collector) Register(mux *http.ServeMux) {
	mux.Handle("GET /metrics", c.Handler())
}
