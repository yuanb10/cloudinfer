package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
	"github.com/prometheus/common/expfmt"
)

type debugTestStreamer struct {
	name          string
	resolvedModel string
}

func (s debugTestStreamer) Name() string {
	return s.name
}

func (s debugTestStreamer) StreamText(context.Context, string, []routing.Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error)
	close(tokenCh)
	close(errCh)
	return tokenCh, errCh
}

func (s debugTestStreamer) ResolvedModel(string) string {
	return s.resolvedModel
}

func (s debugTestStreamer) Close() error {
	return nil
}

func TestHealthzHandler(t *testing.T) {
	cfg := &config.Config{}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	runtime := NewRuntimeState(false, nil)
	runtime.SetListenerReady()
	s := NewServer(cfg, logger, collector, nil, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	if rr.Body.String() != "ok" {
		t.Fatalf("handler returned unexpected body: got %v want %v", rr.Body.String(), "ok")
	}
}

func TestReadyzReturnsMockModeWhenNoBackendsConfigured(t *testing.T) {
	runtime := NewRuntimeState(false, nil)
	runtime.SetListenerReady()
	server := newTestServer(t, nil, runtime)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	var body readyResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if !body.Ready {
		t.Fatal("ready = false, want true")
	}
	if body.Mode != "mock" {
		t.Fatalf("mode = %q, want %q", body.Mode, "mock")
	}
}

func TestReadyzReturnsServiceUnavailableWhenAllBackendsFailedInit(t *testing.T) {
	runtime := NewRuntimeState(true, []BackendStatus{
		{Name: "alpha", Type: "openai", DefaultModel: "gpt-4o-mini", InitError: "missing api key"},
		{Name: "beta", Type: "vertex", DefaultModel: "gemini-2.0-flash", InitError: "missing adc"},
	})
	runtime.SetListenerReady()
	server := newTestServer(t, nil, runtime)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}

	var body readyResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body.Ready {
		t.Fatal("ready = true, want false")
	}
	if body.InitializedBackends != 0 {
		t.Fatalf("initialized_backends = %d, want 0", body.InitializedBackends)
	}
}

func TestDebugRoutesReturnsStatsAndNoSecrets(t *testing.T) {
	stats := routing.NewStatsStore(0.2, 15*time.Second)
	stats.Observe("alpha", time.Now(), "ok", 42, false)

	router := routing.NewRouter(
		[]routing.Backend{
			{Name: "alpha", Type: "openai", Client: debugTestStreamer{name: "alpha", resolvedModel: "gpt-4o-mini"}},
		},
		stats,
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1},
	)

	runtime := NewRuntimeState(true, []BackendStatus{
		{Name: "alpha", Type: "openai", DefaultModel: "gpt-4o-mini", Initialized: true},
		{Name: "beta", Type: "openai", DefaultModel: "gpt-4.1-mini", InitError: "missing api key"},
	})
	runtime.SetListenerReady()
	server := newTestServer(t, router, runtime)

	req := httptest.NewRequest(http.MethodGet, "/debug/routes", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	if strings.Contains(rr.Body.String(), "sk-live") {
		t.Fatal("debug routes leaked a secret value")
	}

	var body debugRoutesResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if len(body.Backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(body.Backends))
	}
	if body.Current.Reason == "" {
		t.Fatal("current route reason is empty")
	}
	if body.Backends[0].Stats.Samples != 1 {
		t.Fatalf("samples = %d, want 1", body.Backends[0].Stats.Samples)
	}
	if body.Backends[0].Breaker.State == "" {
		t.Fatal("breaker state is empty")
	}
	if body.Backends[0].RouteReason == "" {
		t.Fatal("backend route reason is empty")
	}
	if body.Backends[1].Stats.Total != 0 {
		t.Fatalf("failed backend total = %d, want 0", body.Backends[1].Stats.Total)
	}
	if body.Backends[1].Health != "unavailable" {
		t.Fatalf("failed backend health = %q, want %q", body.Backends[1].Health, "unavailable")
	}
}

func TestDebugConfigMasksHeaderValues(t *testing.T) {
	cfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Host:                 "127.0.0.1",
			Port:                 8080,
			ShutdownGraceSeconds: 20,
		},
		OpenAI: config.OpenAIConfig{
			APIKeyEnv: "OPENAI_API_KEY",
			BaseURL:   "https://example.com/v1",
			Model:     "gpt-4o-mini",
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer sk-live-secret",
			},
		},
		Backends: []config.BackendInstance{
			{
				Name: "alpha",
				Type: "openai",
				Routing: config.BackendRoutingConfig{
					TTFTTimeoutMs:       intPtr(20),
					RetryMaxAttempts:    intPtr(1),
					RetryJitterFraction: floatPtr(0.1),
				},
			},
		},
	}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	runtime := NewRuntimeState(false, nil)
	runtime.SetListenerReady()
	s := NewServer(cfg, logger, collector, nil, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/config", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	if strings.Contains(rr.Body.String(), "sk-live-secret") {
		t.Fatal("debug config leaked a secret value")
	}

	var body sanitizedConfig
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body.OpenAI.ExtraHeaders["Authorization"] != "<masked>" {
		t.Fatalf("authorization header = %q, want %q", body.OpenAI.ExtraHeaders["Authorization"], "<masked>")
	}
	if body.Backends[0].Routing.TTFTTimeoutMs == nil || *body.Backends[0].Routing.TTFTTimeoutMs != 20 {
		t.Fatalf("backend routing ttft timeout = %v, want %d", body.Backends[0].Routing.TTFTTimeoutMs, 20)
	}
}

func intPtr(value int) *int {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func TestDebugEndpointsRejectNonLoopbackByDefault(t *testing.T) {
	server := newTestServer(t, nil, readyRuntime())

	req := httptest.NewRequest(http.MethodGet, "/debug/config", nil)
	req.RemoteAddr = "203.0.113.10:4444"
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestDebugEndpointsAllowRemoteWhenExposed(t *testing.T) {
	t.Setenv("CLOUDINFER_DEBUG_TOKEN", "debug-secret")

	cfg := &config.Config{
		ServerConfig: config.ServerConfig{
			DebugExpose:       true,
			DebugAuthTokenEnv: "CLOUDINFER_DEBUG_TOKEN",
		},
	}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	runtime := readyRuntime()
	s := NewServer(cfg, logger, collector, nil, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/routes", nil)
	req.RemoteAddr = "203.0.113.10:4444"
	req.Header.Set("Authorization", "Bearer debug-secret")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestDebugEndpointsRejectRemoteWhenExposedWithoutAuthConfig(t *testing.T) {
	cfg := &config.Config{
		ServerConfig: config.ServerConfig{
			DebugExpose: true,
		},
	}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	runtime := readyRuntime()
	s := NewServer(cfg, logger, collector, nil, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/routes", nil)
	req.RemoteAddr = "203.0.113.10:4444"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestDebugEndpointsDoNotEchoRequestPayload(t *testing.T) {
	server := newTestServer(t, nil, readyRuntime())
	payload := `{"messages":[{"role":"user","content":"top-secret"}],"temperature":0.9}`

	for _, path := range []string{"/debug/config", "/debug/routes"} {
		req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()
		server.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("%s status code = %d, want %d", path, rr.Code, http.StatusOK)
		}
		if strings.Contains(rr.Body.String(), "top-secret") {
			t.Fatalf("%s response echoed request content", path)
		}
		if strings.Contains(rr.Body.String(), "\"messages\"") {
			t.Fatalf("%s response echoed request payload fields", path)
		}
	}
}

func TestMetricsEndpointExposesRequiredMetricsAndNoForbiddenLabels(t *testing.T) {
	httpServer := httptest.NewServer(newTestServer(t, nil, readyRuntime()))
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
}

func newTestServer(t *testing.T, router *routing.Router, runtime *RuntimeState) *http.ServeMux {
	t.Helper()

	cfg := &config.Config{}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	s := NewServer(cfg, logger, collector, router, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	return mux
}

func readyRuntime() *RuntimeState {
	runtime := NewRuntimeState(false, nil)
	runtime.SetListenerReady()
	return runtime
}
