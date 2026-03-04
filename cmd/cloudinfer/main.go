package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	adapteropenai "github.com/myusername/cloudinfer/internal/adapters/openai"
	adaptervertex "github.com/myusername/cloudinfer/internal/adapters/vertex"
	"github.com/myusername/cloudinfer/internal/api"
	"github.com/myusername/cloudinfer/internal/backends/wrap"
	"github.com/myusername/cloudinfer/internal/config"
	tracinghttp "github.com/myusername/cloudinfer/internal/http"
	"github.com/myusername/cloudinfer/internal/lifecycle"
	"github.com/myusername/cloudinfer/internal/metrics"
	otelbootstrap "github.com/myusername/cloudinfer/internal/otel"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	log.Printf("configuration loaded, server address=%s", cfg.Address())

	shutdownTracing, err := otelbootstrap.Bootstrap(context.Background())
	if err != nil {
		log.Fatalf("initialize opentelemetry: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Printf("shutdown opentelemetry: %v", err)
		}
	}()

	mux := http.NewServeMux()
	collector := metrics.New()
	logger := telemetry.NewJSONStdoutLogger()
	backendsStatus := make([]api.BackendStatus, 0, len(cfg.Backends))
	drainState := lifecycle.NewDrainState()

	routingBackends := make([]routing.Backend, 0, len(cfg.Backends))
	for _, backendCfg := range cfg.Backends {
		backendStatus := api.BackendStatus{
			Name: backendCfg.Name,
			Type: backendCfg.Type,
		}
		backendPolicy := buildBackendRoutingPolicy(cfg.Routing, backendCfg.Routing)

		switch backendCfg.Type {
		case "vertex":
			backendStatus.DefaultModel = backendCfg.Vertex.Model
			vertexAdapter, initErr := adaptervertex.New(context.Background(), backendCfg.Vertex)
			if initErr != nil {
				backendStatus.InitError = initErr.Error()
				backendsStatus = append(backendsStatus, backendStatus)
				log.Printf("initialize vertex adapter %q: %v", backendCfg.Name, initErr)
				continue
			}
			streamer := wrap.NewAdapterStreamer(backendCfg.Name, backendCfg.Vertex.Model, vertexAdapter)
			backendStatus.Initialized = true
			backendsStatus = append(backendsStatus, backendStatus)
			routingBackends = append(routingBackends, routing.Backend{
				Name:   backendCfg.Name,
				Type:   backendCfg.Type,
				Client: streamer,
				Policy: backendPolicy,
			})
			defer func(name string, client routing.Streamer) {
				if err := client.Close(); err != nil {
					log.Printf("close backend %s: %v", name, err)
				}
			}(backendCfg.Name, streamer)
		case "openai":
			backendStatus.DefaultModel = backendCfg.OpenAI.Model
			openAIAdapter, initErr := adapteropenai.New(backendCfg.OpenAI)
			if initErr != nil {
				backendStatus.InitError = initErr.Error()
				backendsStatus = append(backendsStatus, backendStatus)
				log.Printf("initialize openai-compatible adapter %q: %v", backendCfg.Name, initErr)
				continue
			}
			streamer := wrap.NewAdapterStreamer(backendCfg.Name, backendCfg.OpenAI.Model, openAIAdapter)
			backendStatus.Initialized = true
			backendsStatus = append(backendsStatus, backendStatus)
			routingBackends = append(routingBackends, routing.Backend{
				Name:   backendCfg.Name,
				Type:   backendCfg.Type,
				Client: streamer,
				Policy: backendPolicy,
			})
			defer func(name string, client routing.Streamer) {
				if err := client.Close(); err != nil {
					log.Printf("close backend %s: %v", name, err)
				}
			}(backendCfg.Name, streamer)
		}
	}

	var router *routing.Router
	if len(routingBackends) > 0 {
		effectiveRoutingEnabled := cfg.EffectiveRoutingEnabled()
		stats := routing.NewStatsStore(
			cfg.Routing.EwmaAlpha,
			time.Duration(cfg.Routing.CooldownSeconds)*time.Second,
		).WithCooldownJitter(cfg.Routing.CooldownJitterFraction, rand.Float64)
		router = routing.NewRouter(routingBackends, stats, routing.PolicyConfig{
			Enabled:         effectiveRoutingEnabled,
			Policy:          cfg.Routing.Policy,
			CooldownSeconds: cfg.Routing.CooldownSeconds,
			EwmaAlpha:       cfg.Routing.EwmaAlpha,
			MinSamples:      int64(cfg.Routing.MinSamples),
			Prefer:          cfg.Routing.Prefer,
			Breaker: routing.BreakerConfig{
				ConsecutiveFailures:   cfg.Routing.BreakerConsecutiveFailures,
				WindowSize:            cfg.Routing.BreakerWindowSize,
				FailureRateThreshold:  cfg.Routing.BreakerFailureRate,
				HalfOpenProbeInterval: time.Duration(cfg.Routing.BreakerHalfOpenProbeIntervalMs) * time.Millisecond,
			},
			Retry: routing.RetryPolicy{
				MaxAttempts:    cfg.Routing.RetryMaxAttempts,
				BaseBackoff:    time.Duration(cfg.Routing.RetryBaseBackoffMs) * time.Millisecond,
				MaxBackoff:     time.Duration(cfg.Routing.RetryMaxBackoffMs) * time.Millisecond,
				JitterFraction: cfg.Routing.RetryJitterFraction,
				JitterSource:   rand.Float64,
			},
			Timeouts: routing.TimeoutPolicy{
				Total: time.Duration(cfg.Routing.TotalTimeoutMs) * time.Millisecond,
				TTFT:  time.Duration(cfg.Routing.TTFTTimeoutMs) * time.Millisecond,
				Idle:  time.Duration(cfg.Routing.IdleTimeoutMs) * time.Millisecond,
			},
		})
	}

	runtime := api.NewRuntimeState(cfg.EffectiveRoutingEnabled(), backendsStatus)
	api.NewServer(&cfg, logger, collector, router, runtime, drainState).RegisterRoutes(mux)

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           tracinghttp.NewTracingMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Address(), err)
	}
	runtime.SetListenerReady()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("READY: starting cloudinfer server on %s", server.Addr)
		serverErr <- server.Serve(ln)
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
		log.Printf("server stopped")
	case <-ctx.Done():
		gracePeriod := cfg.ShutdownGracePeriod()
		drainDeadline := time.Now().Add(gracePeriod)
		drainStart := time.Now()
		log.Printf("DRAINING_START: shutdown signal received, initiating drain with grace period=%s", gracePeriod)

		runtime.StartDrain()
		drainState.StartDrain(drainDeadline)
		collector.SetDraining(true)

		drainWaitCtx, drainCancel := context.WithDeadline(context.Background(), drainDeadline)
		drained := drainState.Wait(drainWaitCtx)
		drainCancel()
		if drained {
			log.Printf("DRAINING_DONE: all streams completed")
		} else {
			log.Printf("SHUTDOWN_FORCED: drain deadline reached with %d streams still active", drainState.InFlight())
		}

		minDrainWindow := 1 * time.Second
		if remaining := time.Until(drainStart.Add(minDrainWindow)); remaining > 0 {
			time.Sleep(remaining)
		}

		shutdownTimeout := 5 * time.Second
		if remaining := time.Until(drainDeadline); remaining > 0 && remaining < shutdownTimeout {
			shutdownTimeout = remaining
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("SHUTDOWN_FORCED: graceful shutdown did not complete cleanly: %v", err)
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				log.Printf("force close failed: %v", closeErr)
			}
		}

		log.Printf("server shutdown completed")
	}
}

func buildBackendRoutingPolicy(global config.RoutingConfig, override config.BackendRoutingConfig) routing.BackendPolicy {
	policy := routing.BackendPolicy{
		Breaker: routing.BreakerConfig{
			ConsecutiveFailures:   global.BreakerConsecutiveFailures,
			WindowSize:            global.BreakerWindowSize,
			FailureRateThreshold:  global.BreakerFailureRate,
			HalfOpenProbeInterval: time.Duration(global.BreakerHalfOpenProbeIntervalMs) * time.Millisecond,
		},
		Retry: routing.RetryPolicy{
			MaxAttempts:    global.RetryMaxAttempts,
			BaseBackoff:    time.Duration(global.RetryBaseBackoffMs) * time.Millisecond,
			MaxBackoff:     time.Duration(global.RetryMaxBackoffMs) * time.Millisecond,
			JitterFraction: global.RetryJitterFraction,
			JitterSource:   rand.Float64,
		},
		Timeouts: routing.TimeoutPolicy{
			Total: time.Duration(global.TotalTimeoutMs) * time.Millisecond,
			TTFT:  time.Duration(global.TTFTTimeoutMs) * time.Millisecond,
			Idle:  time.Duration(global.IdleTimeoutMs) * time.Millisecond,
		},
	}

	if override.TotalTimeoutMs != nil {
		policy.Timeouts.Total = time.Duration(*override.TotalTimeoutMs) * time.Millisecond
	}
	if override.TTFTTimeoutMs != nil {
		policy.Timeouts.TTFT = time.Duration(*override.TTFTTimeoutMs) * time.Millisecond
	}
	if override.IdleTimeoutMs != nil {
		policy.Timeouts.Idle = time.Duration(*override.IdleTimeoutMs) * time.Millisecond
	}
	if override.BreakerConsecutiveFailures != nil {
		policy.Breaker.ConsecutiveFailures = *override.BreakerConsecutiveFailures
	}
	if override.BreakerWindowSize != nil {
		policy.Breaker.WindowSize = *override.BreakerWindowSize
	}
	if override.BreakerFailureRate != nil {
		policy.Breaker.FailureRateThreshold = *override.BreakerFailureRate
	}
	if override.BreakerHalfOpenProbeIntervalMs != nil {
		policy.Breaker.HalfOpenProbeInterval = time.Duration(*override.BreakerHalfOpenProbeIntervalMs) * time.Millisecond
	}
	if override.RetryMaxAttempts != nil {
		policy.Retry.MaxAttempts = *override.RetryMaxAttempts
	}
	if override.RetryBaseBackoffMs != nil {
		policy.Retry.BaseBackoff = time.Duration(*override.RetryBaseBackoffMs) * time.Millisecond
	}
	if override.RetryMaxBackoffMs != nil {
		policy.Retry.MaxBackoff = time.Duration(*override.RetryMaxBackoffMs) * time.Millisecond
	}
	if override.RetryJitterFraction != nil {
		policy.Retry.JitterFraction = *override.RetryJitterFraction
	}

	return policy
}
