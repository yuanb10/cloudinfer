package api

import (
	"encoding/json"
	"net/http"

	"github.com/myusername/cloudinfer/internal/backends/vertex"
	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type Server struct {
	cfg     *config.Config
	logger  telemetry.Logger
	metrics *metrics.Collector
	vertex  *vertex.VertexAdapter
}

func NewServer(cfg *config.Config, logger telemetry.Logger, collector *metrics.Collector, vertexAdapter *vertex.VertexAdapter) *Server {
	return &Server{
		cfg:     cfg,
		logger:  logger,
		metrics: collector,
		vertex:  vertexAdapter,
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /healthz", s.healthHandler)
	mux.HandleFunc("GET /readyz", s.readyHandler)
	if s.metrics != nil {
		s.metrics.Register(mux)
	}
}

func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(payload)
}
