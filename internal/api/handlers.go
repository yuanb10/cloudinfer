package api

import (
	"encoding/json"
	"net/http"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type Server struct {
	cfg     *config.Config
	logger  telemetry.Logger
	metrics *metrics.Collector
	router  *routing.Router
	runtime RuntimeState
}

func NewServer(cfg *config.Config, logger telemetry.Logger, collector *metrics.Collector, router *routing.Router, runtime RuntimeState) *Server {
	return &Server{
		cfg:     cfg,
		logger:  logger,
		metrics: collector,
		router:  router,
		runtime: runtime,
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /healthz", s.healthHandler)
	mux.HandleFunc("GET /readyz", s.readyHandler)
	mux.HandleFunc("GET /debug/routes", s.debugRoutesHandler)
	mux.HandleFunc("GET /debug/config", s.debugConfigHandler)
	if s.metrics != nil {
		s.metrics.Register(mux)
	}
}

func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) readyHandler(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	if !s.runtime.Ready() {
		status = http.StatusServiceUnavailable
	}

	_ = writeJSON(w, status, readyResponse{
		Ready:               s.runtime.Ready(),
		Mode:                s.runtime.Mode(),
		EffectiveRouting:    s.runtime.RoutingEnabled,
		ConfiguredBackends:  s.runtime.ConfiguredBackends(),
		InitializedBackends: s.runtime.InitializedBackends(),
		FailedBackends:      s.runtime.FailedBackends(),
		Backends:            s.runtime.Backends,
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(payload)
}

type readyResponse struct {
	Ready               bool            `json:"ready"`
	Mode                string          `json:"mode"`
	EffectiveRouting    bool            `json:"effective_routing_enabled"`
	ConfiguredBackends  int             `json:"configured_backends"`
	InitializedBackends int             `json:"initialized_backends"`
	FailedBackends      int             `json:"failed_backends"`
	Backends            []BackendStatus `json:"backends"`
}
