package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/api"
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/lifecycle"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/routing"
)

func TestDrainDropsReadinessAndDrainsStream(t *testing.T) {
	grace := 2 * time.Second
	addr, server, runtime, drain, streamer, serverErr := startIntegrationServer(t, grace)
	client := &http.Client{Timeout: 5 * time.Second}

	streamReq := newStreamRequest(addr)
	streamResp, err := client.Do(streamReq)
	if err != nil {
		t.Fatalf("start stream: %v", err)
	}
	defer streamResp.Body.Close()

	waitForSignal(t, streamer.started, 1*time.Second, "streamer to start")

	drainDeadline := time.Now().Add(grace)
	runtime.StartDrain()
	drain.StartDrain(drainDeadline)

	assertReadyzStatus(t, client, addr, http.StatusServiceUnavailable, time.Second)

	// New streams are rejected while draining.
	secondResp, err := client.Do(newStreamRequest(addr))
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", secondResp.StatusCode, http.StatusServiceUnavailable)
	}

	// Let the original stream finish before the grace deadline.
	close(streamer.unblock)
	if _, err := io.Copy(io.Discard, streamResp.Body); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("draining stream: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("server shutdown: %v", err)
	}

	waitCtx, waitCancel := context.WithDeadline(context.Background(), drainDeadline)
	defer waitCancel()
	if !drain.Wait(waitCtx) {
		t.Fatalf("drain did not finish before deadline; inFlight=%d", drain.InFlight())
	}

	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("server error: %v", err)
	}
}

func newStreamRequest(addr string) *http.Request {
	body := strings.NewReader(`{"model":"alpha-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

type blockingStreamer struct {
	started chan struct{}
	unblock chan struct{}
}

func newBlockingStreamer() *blockingStreamer {
	return &blockingStreamer{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (s *blockingStreamer) Name() string { return "alpha" }

func (s *blockingStreamer) StreamText(ctx context.Context, _ string, _ []routing.Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)
		close(s.started)
		tokenCh <- "hi"

		select {
		case <-s.unblock:
			return
		case <-ctx.Done():
			errCh <- ctx.Err()
			return
		}
	}()

	return tokenCh, errCh
}

func (s *blockingStreamer) ResolvedModel(string) string { return "alpha-model" }
func (s *blockingStreamer) Close() error                { return nil }

func startIntegrationServer(t *testing.T, grace time.Duration) (string, *http.Server, *api.RuntimeState, *lifecycle.DrainState, *blockingStreamer, chan error) {
	t.Helper()

	cfg := config.Config{
		ServerConfig: config.ServerConfig{
			Host:                 "127.0.0.1",
			Port:                 0,
			ShutdownGraceSeconds: int(grace / time.Second),
		},
		Routing: config.RoutingConfig{
			Enabled:    true,
			EnabledSet: true,
		},
	}

	drain := lifecycle.NewDrainState()
	streamer := newBlockingStreamer()
	router := routing.NewRouter(
		[]routing.Backend{{Name: "alpha", Type: "openai", Client: streamer}},
		routing.NewStatsStore(0.2, 15*time.Second),
		routing.PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 1},
	)
	runtime := api.NewRuntimeState(true, []api.BackendStatus{
		{Name: "alpha", Type: "openai", Initialized: true},
	})

	mux := http.NewServeMux()
	api.NewServer(&cfg, nil, metrics.New(), router, runtime, drain).RegisterRoutes(mux)

	server := &http.Server{Addr: cfg.Address(), Handler: mux}
	ln, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	runtime.SetListenerReady()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ln)
	}()

	return ln.Addr().String(), server, runtime, drain, streamer, errCh
}

func assertReadyzStatus(t *testing.T, client *http.Client, addr string, wantStatus int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/readyz", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == wantStatus {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("/readyz did not reach status %d within %s", wantStatus, timeout)
}

func waitForSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, reason string) {
	t.Helper()
	select {
	case <-ch:
		return
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", reason)
	}
}
