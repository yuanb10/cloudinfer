package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	openaitypes "github.com/myusername/cloudinfer/internal/openai"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type scriptedStreamer struct {
	name             string
	resolvedModel    string
	tokens           []string
	delayBeforeFirst time.Duration
	delayBetween     time.Duration
	terminalErr      error
}

func (s scriptedStreamer) Name() string {
	return s.name
}

func (s scriptedStreamer) StreamText(ctx context.Context, _ string, _ []routing.Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		for index, token := range s.tokens {
			delay := s.delayBetween
			if index == 0 {
				delay = s.delayBeforeFirst
			}
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}

			select {
			case <-ctx.Done():
				return
			case tokenCh <- token:
			}
		}

		if s.terminalErr != nil {
			errCh <- s.terminalErr
		}
	}()

	return tokenCh, errCh
}

func (s scriptedStreamer) ResolvedModel(string) string {
	if s.resolvedModel != "" {
		return s.resolvedModel
	}

	return "test-model"
}

func (s scriptedStreamer) Close() error {
	return nil
}

type captureLogger struct {
	mu     sync.Mutex
	events []telemetry.TelemetryEvent
}

func (l *captureLogger) Log(evt telemetry.TelemetryEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, evt)
}

func (l *captureLogger) Last() telemetry.TelemetryEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.events) == 0 {
		return telemetry.TelemetryEvent{}
	}

	return l.events[len(l.events)-1]
}

type parsedSSEEvent struct {
	Name string
	Data string
}

type cancelAwareStreamer struct {
	started  chan struct{}
	canceled chan struct{}
	done     chan struct{}
}

func (s *cancelAwareStreamer) Name() string {
	return "cancel-aware"
}

func (s *cancelAwareStreamer) StreamText(ctx context.Context, _ string, _ []routing.Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error)

	go func() {
		defer close(s.done)
		defer close(tokenCh)
		defer close(errCh)
		close(s.started)

		<-ctx.Done()
		close(s.canceled)
		time.Sleep(25 * time.Millisecond)
	}()

	return tokenCh, errCh
}

func (s *cancelAwareStreamer) ResolvedModel(string) string {
	return "cancel-model"
}

func (s *cancelAwareStreamer) Close() error {
	return nil
}

func TestParseResponsesRequestAcceptsTextParts(t *testing.T) {
	req, err := parseResponsesRequest(strings.NewReader(`{
		"model":"gpt-4.1-mini",
		"stream":true,
		"input":[{"role":"user","content":[{"type":"input_text","text":"hello "},{"type":"input_text","text":"world"}]}]
	}`))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	if req.Model != "gpt-4.1-mini" {
		t.Fatalf("model = %q, want %q", req.Model, "gpt-4.1-mini")
	}
	if !req.Stream {
		t.Fatal("stream = false, want true")
	}
	if len(req.Input) != 1 {
		t.Fatalf("input length = %d, want 1", len(req.Input))
	}
	if req.Input[0].Content != "hello world" {
		t.Fatalf("content = %q, want %q", req.Input[0].Content, "hello world")
	}
}

func TestParseResponsesRequestRejectsUnsupportedField(t *testing.T) {
	_, err := parseResponsesRequest(strings.NewReader(`{
		"input":[{"role":"user","content":"hi"}],
		"tools":[]
	}`))
	if err == nil {
		t.Fatal("expected unsupported field error")
	}
	if !strings.Contains(err.Error(), `unsupported field "tools"`) {
		t.Fatalf("error = %q, want unsupported field message", err.Error())
	}
}

func TestResponsesNonStreamReturnsResponseObject(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"input":[{"role":"user","content":"Say hello"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Request-Id"); got == "" {
		t.Fatal("x-request-id header is empty")
	}

	var body openaitypes.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Object != "response" {
		t.Fatalf("object = %q, want %q", body.Object, "response")
	}
	if body.Status != "completed" {
		t.Fatalf("status = %q, want %q", body.Status, "completed")
	}
	if body.OutputText != "hello" {
		t.Fatalf("output_text = %q, want %q", body.OutputText, "hello")
	}
	if len(body.Output) != 1 {
		t.Fatalf("output length = %d, want 1", len(body.Output))
	}
	if got := body.Output[0].Role; got != "assistant" {
		t.Fatalf("output[0].role = %q, want %q", got, "assistant")
	}
	if len(body.Output[0].Content) != 1 {
		t.Fatalf("output[0].content length = %d, want 1", len(body.Output[0].Content))
	}
	if got := body.Output[0].Content[0].Text; got != "hello" {
		t.Fatalf("output[0].content[0].text = %q, want %q", got, "hello")
	}
}

func TestResponsesStreamConformance(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"stream":true,
		"input":[{"role":"user","content":"Say hello"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
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
	if strings.Contains(bodyText, "[DONE]") {
		t.Fatalf("responses stream unexpectedly contained done marker: %q", bodyText)
	}

	events := parseSSEEvents(t, bodyText)
	wantNames := []string{
		"response.created",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.completed",
	}
	if len(events) != len(wantNames) {
		t.Fatalf("event count = %d, want %d", len(events), len(wantNames))
	}
	for index, want := range wantNames {
		if events[index].Name != want {
			t.Fatalf("event[%d] = %q, want %q", index, events[index].Name, want)
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(events[index].Data), &payload); err != nil {
			t.Fatalf("unmarshal event[%d]: %v", index, err)
		}
		if got := payload["type"]; got != want {
			t.Fatalf("payload type for event[%d] = %v, want %q", index, got, want)
		}
	}

	var firstDelta openaitypes.ResponseOutputTextDeltaEvent
	if err := json.Unmarshal([]byte(events[1].Data), &firstDelta); err != nil {
		t.Fatalf("unmarshal first delta: %v", err)
	}
	var secondDelta openaitypes.ResponseOutputTextDeltaEvent
	if err := json.Unmarshal([]byte(events[2].Data), &secondDelta); err != nil {
		t.Fatalf("unmarshal second delta: %v", err)
	}
	if firstDelta.SequenceNumber < 0 {
		t.Fatalf("first delta sequence_number = %d, want non-negative", firstDelta.SequenceNumber)
	}
	if secondDelta.SequenceNumber <= firstDelta.SequenceNumber {
		t.Fatalf("second delta sequence_number = %d, want greater than %d", secondDelta.SequenceNumber, firstDelta.SequenceNumber)
	}

	var completed openaitypes.ResponseCompletedEvent
	if err := json.Unmarshal([]byte(events[len(events)-1].Data), &completed); err != nil {
		t.Fatalf("unmarshal completed event: %v", err)
	}
	if completed.Response.OutputText != "hello" {
		t.Fatalf("completed output_text = %q, want %q", completed.Response.OutputText, "hello")
	}
}

func TestResponsesStreamEmitsErrorEventOnUpstreamFailure(t *testing.T) {
	server := newScriptedResponsesServer(t, scriptedStreamer{
		name:          "alpha",
		resolvedModel: "alpha-model",
		tokens:        []string{"he"},
		terminalErr:   errors.New("boom"),
	}, nil)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"stream":true,
		"input":[{"role":"user","content":"hi"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	events := parseSSEEvents(t, string(body))
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if events[2].Name != "error" {
		t.Fatalf("final event = %q, want %q", events[2].Name, "error")
	}

	var errEvent openaitypes.ErrorEvent
	if err := json.Unmarshal([]byte(events[2].Data), &errEvent); err != nil {
		t.Fatalf("unmarshal error event: %v", err)
	}
	if errEvent.Error.Message != "upstream stream failed" {
		t.Fatalf("error message = %q, want %q", errEvent.Error.Message, "upstream stream failed")
	}
	if errEvent.Error.Type != "server_error" {
		t.Fatalf("error type = %q, want %q", errEvent.Error.Type, "server_error")
	}
}

func TestResponsesStreamTTFTMatchesFirstDeltaTiming(t *testing.T) {
	logger := &captureLogger{}
	server := newScriptedResponsesServer(t, scriptedStreamer{
		name:             "alpha",
		resolvedModel:    "alpha-model",
		tokens:           []string{"he", "llo"},
		delayBeforeFirst: 60 * time.Millisecond,
		delayBetween:     5 * time.Millisecond,
	}, logger)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"stream":true,
		"input":[{"role":"user","content":"hi"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	events := parseSSEEvents(t, string(body))
	if len(events) < 3 {
		t.Fatalf("event count = %d, want at least 3", len(events))
	}

	var firstDelta openaitypes.ResponseOutputTextDeltaEvent
	if err := json.Unmarshal([]byte(events[1].Data), &firstDelta); err != nil {
		t.Fatalf("unmarshal first delta: %v", err)
	}
	if firstDelta.Metrics == nil {
		t.Fatal("first delta metrics are missing")
	}
	if firstDelta.Metrics.TTFTms < 50 {
		t.Fatalf("ttft_ms = %d, want at least 50", firstDelta.Metrics.TTFTms)
	}
	if firstDelta.Metrics.TTFTms != firstDelta.Metrics.ChunkMs {
		t.Fatalf("first delta chunk_ms = %d, want equal to ttft_ms = %d", firstDelta.Metrics.ChunkMs, firstDelta.Metrics.TTFTms)
	}

	last := logger.Last()
	if last.Endpoint != responsesEndpoint {
		t.Fatalf("telemetry endpoint = %q, want %q", last.Endpoint, responsesEndpoint)
	}
	if last.TTFTms != firstDelta.Metrics.TTFTms {
		t.Fatalf("telemetry ttft_ms = %d, want %d", last.TTFTms, firstDelta.Metrics.TTFTms)
	}
}

func TestResponsesStreamFallsBackBeforeFirstTokenOnTTFTTimeout(t *testing.T) {
	router := routing.NewRouter(
		[]routing.Backend{
			{
				Name: "alpha",
				Type: "openai",
				Client: scriptedStreamer{
					name:             "alpha",
					resolvedModel:    "slow-model",
					tokens:           []string{"slow"},
					delayBeforeFirst: 75 * time.Millisecond,
				},
			},
			{
				Name: "beta",
				Type: "vertex",
				Client: scriptedStreamer{
					name:          "beta",
					resolvedModel: "fast-model",
					tokens:        []string{"fast"},
				},
			},
		},
		routing.NewStatsStore(0.2, 50*time.Millisecond).WithCooldownJitter(0, nil),
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1, Prefer: "alpha"},
	)

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			TTFTTimeoutMs:          20,
			CooldownJitterFraction: 0,
		},
	}
	runtime := NewRuntimeState(true, []BackendStatus{
		{Name: "alpha", Type: "openai", DefaultModel: "slow-model", Initialized: true},
		{Name: "beta", Type: "vertex", DefaultModel: "fast-model", Initialized: true},
	})
	runtime.SetListenerReady()

	mux := http.NewServeMux()
	NewServer(cfg, noopLogger{}, metrics.New(), router, runtime, nil).RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"stream":true,
		"input":[{"role":"user","content":"Say hello"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-CloudInfer-Backend"); got != "beta" {
		t.Fatalf("x-cloudinfer-backend = %q, want %q", got, "beta")
	}
	if got := resp.Header.Get("X-CloudInfer-Model"); got != "fast-model" {
		t.Fatalf("x-cloudinfer-model = %q, want %q", got, "fast-model")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	bodyText := string(body)
	if strings.Contains(bodyText, `"delta":"slow"`) {
		t.Fatalf("stream body leaked slow backend output: %q", bodyText)
	}
	if !strings.Contains(bodyText, `"delta":"fast"`) {
		t.Fatalf("stream body missing fast backend output: %q", bodyText)
	}

	snapshots := router.Snapshot()
	if got := snapshots["alpha"].LastStatus; got != "timeout" {
		t.Fatalf("alpha last_status = %q, want %q", got, "timeout")
	}
	if got := snapshots["beta"].LastStatus; got != "ok" {
		t.Fatalf("beta last_status = %q, want %q", got, "ok")
	}
}

func TestResponsesStreamClientDisconnectCancelsUpstream(t *testing.T) {
	streamer := &cancelAwareStreamer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		done:     make(chan struct{}),
	}
	server := newCancelableResponsesServer(t, streamer)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"default",
		"stream":true,
		"input":[{"role":"user","content":"hi"}]
	}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}

	select {
	case <-streamer.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("streamer did not start")
	}

	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read initial response byte: %v", err)
	}

	cancel()
	_ = resp.Body.Close()

	select {
	case <-streamer.canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream context was not canceled after client disconnect")
	}

	select {
	case <-streamer.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("streamer goroutine did not exit after client disconnect")
	}
}

func TestResponsesPerformanceSmoke(t *testing.T) {
	mux := http.NewServeMux()
	NewServer(&config.Config{}, noopLogger{}, metrics.New(), nil, readyRuntime(), nil).RegisterRoutes(mux)
	body := `{"model":"default","input":[{"role":"user","content":"hi"}]}`

	const (
		requestCount = 200
		maxDuration  = 500 * time.Millisecond
		maxAllocs    = 250.0
	)

	allocs := testing.AllocsPerRun(25, func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		res := rr.Result()
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			panic("unexpected status " + strconv.Itoa(res.StatusCode))
		}
		_, _ = io.Copy(io.Discard, res.Body)
	})
	if allocs > maxAllocs {
		t.Fatalf("allocs/request = %.1f, want <= %.1f", allocs, maxAllocs)
	}

	start := time.Now()
	for i := 0; i < requestCount; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		res := rr.Result()
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			t.Fatalf("request %d status = %d, want %d", i, res.StatusCode, http.StatusOK)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	elapsed := time.Since(start)
	if elapsed > maxDuration {
		t.Fatalf("%d requests took %s, want <= %s", requestCount, elapsed, maxDuration)
	}

	t.Logf("performance smoke: %d requests in %s, allocs/request=%.1f", requestCount, elapsed, allocs)
}

func newScriptedResponsesServer(t *testing.T, streamer scriptedStreamer, logger telemetry.Logger) *httptest.Server {
	t.Helper()

	if logger == nil {
		logger = noopLogger{}
	}

	router := routing.NewRouter(
		[]routing.Backend{
			{Name: "alpha", Type: "openai", Client: streamer},
		},
		routing.NewStatsStore(0.2, 15*time.Second),
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1, Prefer: "alpha"},
	)

	cfg := &config.Config{}
	collector := metrics.New()
	runtime := NewRuntimeState(true, []BackendStatus{
		{Name: "alpha", Type: "openai", DefaultModel: streamer.ResolvedModel("default"), Initialized: true},
	})
	runtime.SetListenerReady()

	mux := http.NewServeMux()
	NewServer(cfg, logger, collector, router, runtime, nil).RegisterRoutes(mux)

	return httptest.NewServer(mux)
}

func newCancelableResponsesServer(t *testing.T, streamer routing.Streamer) *httptest.Server {
	t.Helper()

	router := routing.NewRouter(
		[]routing.Backend{
			{Name: "alpha", Type: "openai", Client: streamer},
		},
		routing.NewStatsStore(0.2, 15*time.Second),
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1, Prefer: "alpha"},
	)

	cfg := &config.Config{}
	collector := metrics.New()
	runtime := NewRuntimeState(true, []BackendStatus{
		{Name: "alpha", Type: "openai", DefaultModel: streamer.ResolvedModel("default"), Initialized: true},
	})
	runtime.SetListenerReady()

	mux := http.NewServeMux()
	NewServer(cfg, noopLogger{}, collector, router, runtime, nil).RegisterRoutes(mux)

	return httptest.NewServer(mux)
}

func parseSSEEvents(t *testing.T, body string) []parsedSSEEvent {
	t.Helper()

	blocks := strings.Split(body, "\n\n")
	events := make([]parsedSSEEvent, 0, len(blocks))

	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		var evt parsedSSEEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				evt.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				evt.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}

		if evt.Name == "" {
			t.Fatalf("missing event name in block %q", block)
		}
		if evt.Data == "" {
			t.Fatalf("missing data in block %q", block)
		}

		events = append(events, evt)
	}

	return events
}
