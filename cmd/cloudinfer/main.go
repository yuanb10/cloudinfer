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
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
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

	var vertexAdapter *vertex.VertexAdapter
	if cfg.Backend == "vertex" {
		vertexAdapter, err = vertex.NewAdapter(context.Background(), cfg.Vertex)
		if err != nil {
			log.Fatalf("initialize vertex adapter: %v", err)
		}
		defer func() {
			if err := vertexAdapter.Close(); err != nil {
				log.Printf("close vertex adapter: %v", err)
			}
		}()
	}

	var openAIAdapter *openaibackend.Adapter
	if cfg.Backend == "openai" {
		openAIAdapter, err = openaibackend.New(cfg.OpenAI)
		if err != nil {
			log.Fatalf("initialize openai-compatible adapter: %v", err)
		}
		defer func() {
			if err := openAIAdapter.Close(); err != nil {
				log.Printf("close openai-compatible adapter: %v", err)
			}
		}()
	}

	api.NewServer(&cfg, logger, collector, vertexAdapter, openAIAdapter).RegisterRoutes(mux)

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
