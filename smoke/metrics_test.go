package smoke

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/api"
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/telemetry"
	"github.com/prometheus/common/expfmt"
)

func TestMetricsEndpointExposesRequiredMetricsAndNoForbiddenLabels(t *testing.T) {
	httpServer := httptest.NewServer(newSmokeServer())
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		resp.Body.Close()
		t.Fatalf("read streaming response: %v", err)
	}
	resp.Body.Close()

	metricsResp, err := client.Get(httpServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("scrape metrics: %v", err)
	}
	defer metricsResp.Body.Close()

	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status code = %d, want %d", metricsResp.StatusCode, http.StatusOK)
	}
	if got := metricsResp.Header.Get("Content-Type"); got != metrics.MetricsContentType {
		t.Fatalf("metrics content-type = %q, want %q", got, metrics.MetricsContentType)
	}

	bodyBytes, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "cloudinfer_ttft_seconds_bucket{") {
		t.Fatal("metrics body is missing cloudinfer_ttft_seconds_bucket")
	}
	if !strings.Contains(body, "cloudinfer_stream_duration_seconds_bucket{") {
		t.Fatal("metrics body is missing cloudinfer_stream_duration_seconds_bucket")
	}
	if !strings.Contains(body, "cloudinfer_requests_total{") {
		t.Fatal("metrics body is missing cloudinfer_requests_total")
	}
	if !strings.Contains(body, "cloudinfer_draining ") {
		t.Fatal("metrics body is missing cloudinfer_draining")
	}

	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse metrics exposition: %v", err)
	}
	if err := metrics.ValidateMetricMap(families); err != nil {
		t.Fatalf("validate metrics labels: %v", err)
	}
	for _, metricFamily := range families {
		for _, metric := range metricFamily.Metric {
			for _, label := range metric.Label {
				if slices.Contains([]string{"request_id", "user_id"}, label.GetName()) {
					t.Fatalf("metrics unexpectedly exposed forbidden label %q", label.GetName())
				}
			}
		}
	}
}

func newSmokeServer() *http.ServeMux {
	cfg := &config.Config{}
	runtime := api.NewRuntimeState(false, nil)
	runtime.SetListenerReady()

	mux := http.NewServeMux()
	api.NewServer(cfg, telemetry.NewJSONStdoutLogger(), metrics.New(), nil, runtime, nil).RegisterRoutes(mux)
	return mux
}
