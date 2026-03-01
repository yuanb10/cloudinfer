package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type noopLogger struct{}

func (noopLogger) Log(telemetry.TelemetryEvent) {}

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

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg := &config.Config{}
	logger := noopLogger{}
	collector := metrics.New()

	mux := http.NewServeMux()
	NewServer(cfg, logger, collector, nil, nil).RegisterRoutes(mux)

	return httptest.NewServer(mux)
}
