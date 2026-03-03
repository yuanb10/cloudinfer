package metrics

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestMetricsHandlerUsesExactPrometheusTextContentType(t *testing.T) {
	collector := NewWithOptions(Options{DevMode: true})
	collector.ObserveChatCompletion("/v1/chat/completions", "alpha", "alpha-model", "ok", 25*time.Millisecond, 150*time.Millisecond, true)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	collector.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != MetricsContentType {
		t.Fatalf("content-type = %q, want %q", got, MetricsContentType)
	}
}

func TestCollectorExposesOnlyAllowedMetricsAndLabels(t *testing.T) {
	collector := NewWithOptions(Options{DevMode: true})
	collector.ObserveChatCompletion("/v1/chat/completions", "alpha", "alpha-model", "ok", 25*time.Millisecond, 150*time.Millisecond, true)
	collector.SetDraining(true)

	families, err := collector.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	slices.Sort(names)

	wantNames := []string{
		"cloudinfer_draining",
		"cloudinfer_requests_total",
		"cloudinfer_stream_duration_seconds",
		"cloudinfer_ttft_seconds",
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("metric names = %v, want %v", names, wantNames)
	}

	wantLabels := map[string][]string{
		"cloudinfer_requests_total":          {"backend", "endpoint", "status"},
		"cloudinfer_ttft_seconds":            {"backend", "model"},
		"cloudinfer_stream_duration_seconds": {"backend", "model"},
		"cloudinfer_draining":                {},
	}
	if got := AllowedMetricLabels(); !mapsEqual(got, wantLabels) {
		t.Fatalf("allowed labels = %v, want %v", got, wantLabels)
	}

	if err := ValidateMetricFamilies(families); err != nil {
		t.Fatalf("validate metrics: %v", err)
	}
}

func TestValidateMetricFamilyRejectsForbiddenLabels(t *testing.T) {
	family := &dto.MetricFamily{
		Name: stringPtr("cloudinfer_requests_total"),
		Metric: []*dto.Metric{
			{
				Label: []*dto.LabelPair{
					{Name: stringPtr("endpoint"), Value: stringPtr("/v1/chat/completions")},
					{Name: stringPtr("backend"), Value: stringPtr("alpha")},
					{Name: stringPtr("status"), Value: stringPtr("ok")},
					{Name: stringPtr("stream"), Value: stringPtr("true")},
				},
			},
		},
	}

	err := ValidateMetricFamily(family)
	if err == nil {
		t.Fatal("expected forbidden label validation error")
	}
}

func stringPtr(value string) *string {
	return &value
}

func mapsEqual(left map[string][]string, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}

	for key, want := range right {
		got, ok := left[key]
		if !ok {
			return false
		}
		if !slices.Equal(got, want) {
			return false
		}
	}

	return true
}
