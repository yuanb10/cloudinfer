package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/myusername/cloudinfer/internal/api"
	openaibackend "github.com/myusername/cloudinfer/internal/backends/openai"
	"github.com/myusername/cloudinfer/internal/backends/vertex"
	"github.com/myusername/cloudinfer/internal/backends/wrap"
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
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

	mux := http.NewServeMux()
	collector := metrics.New()
	logger := telemetry.NewJSONStdoutLogger()

	routingBackends := make([]routing.Backend, 0, len(cfg.Backends))
	for _, backendCfg := range cfg.Backends {
		switch backendCfg.Type {
		case "vertex":
			vertexAdapter, initErr := vertex.NewAdapter(context.Background(), backendCfg.Vertex)
			if initErr != nil {
				log.Fatalf("initialize vertex adapter %q: %v", backendCfg.Name, initErr)
			}
			streamer := wrap.NewVertexStreamer(backendCfg.Name, backendCfg.Vertex.Model, vertexAdapter)
			routingBackends = append(routingBackends, routing.Backend{
				Name:   backendCfg.Name,
				Type:   backendCfg.Type,
				Client: streamer,
			})
			defer func(name string, client routing.Streamer) {
				if err := client.Close(); err != nil {
					log.Printf("close backend %s: %v", name, err)
				}
			}(backendCfg.Name, streamer)
		case "openai":
			openAIAdapter, initErr := openaibackend.New(backendCfg.OpenAI)
			if initErr != nil {
				log.Fatalf("initialize openai-compatible adapter %q: %v", backendCfg.Name, initErr)
			}
			streamer := wrap.NewOpenAIStreamer(backendCfg.Name, openAIAdapter)
			routingBackends = append(routingBackends, routing.Backend{
				Name:   backendCfg.Name,
				Type:   backendCfg.Type,
				Client: streamer,
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
		)
		router = routing.NewRouter(routingBackends, stats, routing.PolicyConfig{
			Enabled:         effectiveRoutingEnabled,
			Policy:          cfg.Routing.Policy,
			CooldownSeconds: cfg.Routing.CooldownSeconds,
			EwmaAlpha:       cfg.Routing.EwmaAlpha,
			MinSamples:      int64(cfg.Routing.MinSamples),
			Prefer:          cfg.Routing.Prefer,
		})
	}

	api.NewServer(&cfg, logger, collector, router).RegisterRoutes(mux)

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("starting cloudinfer server on %s", server.Addr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
		log.Printf("server stopped")
	case <-ctx.Done():
		log.Printf("shutdown signal received, shutting down server")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("shutdown failed: %v", err)
		}

		log.Printf("server shutdown completed")
	}
}
