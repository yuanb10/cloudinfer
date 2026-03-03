package api

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/lifecycle"
	"github.com/myusername/cloudinfer/internal/metrics"
	"github.com/myusername/cloudinfer/internal/routing"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type Server struct {
	cfg     *config.Config
	logger  telemetry.Logger
	metrics *metrics.Collector
	router  *routing.Router
	runtime *RuntimeState
	lc      *lifecycle.DrainState
}

func NewServer(cfg *config.Config, logger telemetry.Logger, collector *metrics.Collector, router *routing.Router, runtime *RuntimeState, lc *lifecycle.DrainState) *Server {
	return &Server{
		cfg:     cfg,
		logger:  logger,
		metrics: collector,
		router:  router,
		runtime: runtime,
		lc:      lc,
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("GET /healthz", s.healthHandler)
	mux.HandleFunc("GET /readyz", s.readyHandler)
	mux.HandleFunc("GET /debug/routes", s.guardDebugEndpoint(s.debugRoutesHandler))
	mux.HandleFunc("GET /debug/config", s.guardDebugEndpoint(s.debugConfigHandler))
	if s.metrics != nil {
		s.metrics.Register(mux)
	}
}

func (s *Server) guardDebugEndpoint(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackRemoteAddr(r.RemoteAddr) {
			next(w, r)
			return
		}

		if !s.debugEndpointExposed() {
			http.Error(w, "debug endpoint is restricted to localhost", http.StatusForbidden)
			return
		}

		if !s.debugAuthAllowed(r) {
			http.Error(w, "debug endpoint requires configured authentication", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

func (s *Server) debugEndpointExposed() bool {
	return s != nil && s.cfg != nil && s.cfg.DebugExpose
}

func (s *Server) debugAuthAllowed(r *http.Request) bool {
	if s == nil || s.cfg == nil {
		return false
	}

	envName := strings.TrimSpace(s.cfg.DebugAuthTokenEnv)
	if envName == "" {
		return false
	}

	expected := os.Getenv(envName)
	if expected == "" {
		return false
	}

	candidates := []string{
		strings.TrimSpace(r.Header.Get("X-CloudInfer-Debug-Token")),
		strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1 {
			return true
		}
	}

	return false
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

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}

	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
