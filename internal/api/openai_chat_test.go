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
)

type noopLogger struct{}

func (noopLogger) Log(telemetry.TelemetryEvent) {}

type testStreamer struct {
	name          string
	resolvedModel string
	tokens        []string
}

func (s testStreamer) Name() string {
	return s.name
}

func (s testStreamer) StreamText(context.Context, string, []routing.Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string, len(s.tokens))
	errCh := make(chan error)
	for _, token := range s.tokens {
		tokenCh <- token
	}
	close(tokenCh)
	close(errCh)
	return tokenCh, errCh
}

func (s testStreamer) ResolvedModel(string) string {
	if s.resolvedModel != "" {
		return s.resolvedModel
	}

	return "test-model"
}

func (s testStreamer) Close() error {
	return nil
}

func TestChatCompletionsNonStream(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type = %q, want prefix %q", got, "application/json")
	}

	if got := resp.Header.Get("X-Request-Id"); got == "" {
		t.Fatal("x-request-id header is empty")
	}

	var body chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Object != "chat.completion" {
		t.Fatalf("object = %q, want %q", body.Object, "chat.completion")
	}

	if len(body.Choices) == 0 || body.Choices[0].Message == nil {
		t.Fatal("choices[0].message is missing")
	}

	if got := body.Choices[0].Message.Content; got != "hello" {
		t.Fatalf("choices[0].message.content = %q, want %q", got, "hello")
	}
}

func TestChatCompletionsStream(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want prefix %q", got, "text/event-stream")
	}

	if got := resp.Header.Get("X-Request-Id"); got == "" {
		t.Fatal("x-request-id header is empty")
	}

	if got := resp.Header.Get("X-CloudInfer-Backend"); got == "" {
		t.Fatal("x-cloudinfer-backend header is empty")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	bodyText := string(body)
	if !strings.Contains(bodyText, "data: ") {
		t.Fatal("stream body does not contain any SSE data lines")
	}

	if !strings.HasSuffix(bodyText, "data: [DONE]\n\n") {
		t.Fatalf("stream body does not end with done marker: %q", bodyText)
	}
}

func TestChatCompletionsStreamRoutingDisabledOmitsRouteHeaders(t *testing.T) {
	server, router := newDisabledRoutingTestHTTPServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	for _, header := range []string{
		"X-CloudInfer-Backend",
		"X-CloudInfer-Backend-Type",
		"X-CloudInfer-Route-Reason",
		"X-CloudInfer-Model",
	} {
		if got := resp.Header.Get(header); got != "" {
			t.Fatalf("%s = %q, want empty", header, got)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
		t.Fatalf("stream body does not end with done marker: %q", string(body))
	}

	snapshots := router.Snapshot()
	for name, snapshot := range snapshots {
		if snapshot.Total != 0 {
			t.Fatalf("backend %q total observations = %d, want 0 when routing is disabled", name, snapshot.Total)
		}
	}
}

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg := &config.Config{}
	logger := noopLogger{}
	collector := metrics.New()
	router := newTestRouter(true)
	runtime := RuntimeState{
		RoutingEnabled: true,
		Backends: []BackendStatus{
			{Name: "alpha", Type: "openai", DefaultModel: "alpha-model", Initialized: true},
			{Name: "beta", Type: "vertex", DefaultModel: "beta-model", Initialized: true},
		},
	}

	mux := http.NewServeMux()
	NewServer(cfg, logger, collector, router, runtime).RegisterRoutes(mux)

	return httptest.NewServer(mux)
}

func newDisabledRoutingTestHTTPServer(t *testing.T) (*httptest.Server, *routing.Router) {
	t.Helper()

	cfg := &config.Config{}
	logger := noopLogger{}
	collector := metrics.New()
	router := newTestRouter(false)
	runtime := RuntimeState{
		RoutingEnabled: false,
		Backends: []BackendStatus{
			{Name: "alpha", Type: "openai", DefaultModel: "alpha-model", Initialized: true},
			{Name: "beta", Type: "vertex", DefaultModel: "beta-model", Initialized: true},
		},
	}

	mux := http.NewServeMux()
	NewServer(cfg, logger, collector, router, runtime).RegisterRoutes(mux)

	return httptest.NewServer(mux), router
}

func newTestRouter(enabled bool) *routing.Router {
	return routing.NewRouter(
		[]routing.Backend{
			{
				Name:   "alpha",
				Type:   "openai",
				Client: testStreamer{name: "alpha", resolvedModel: "alpha-model", tokens: []string{"he", "llo"}},
			},
			{
				Name:   "beta",
				Type:   "vertex",
				Client: testStreamer{name: "beta", resolvedModel: "beta-model", tokens: []string{"unused"}},
			},
		},
		routing.NewStatsStore(0.2, 15*time.Second),
		routing.PolicyConfig{Enabled: enabled, Policy: "ewma_ttft", MinSamples: 5, Prefer: "alpha"},
	)
}
