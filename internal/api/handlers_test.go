package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
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
	if body.Backends[0].Stats.Samples != 1 {
		t.Fatalf("samples = %d, want 1", body.Backends[0].Stats.Samples)
	}
	if body.Backends[1].Stats.Total != 0 {
		t.Fatalf("failed backend total = %d, want 0", body.Backends[1].Stats.Total)
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
	}
	logger := telemetry.NewJSONStdoutLogger()
	collector := metrics.New()
	runtime := NewRuntimeState(false, nil)
	runtime.SetListenerReady()
	s := NewServer(cfg, logger, collector, nil, runtime, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/config", nil)
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
