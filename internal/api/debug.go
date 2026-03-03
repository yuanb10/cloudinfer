package api

import (
	"net/http"

	"github.com/myusername/cloudinfer/internal/config"
	"github.com/myusername/cloudinfer/internal/routing"
)

type debugRoutesResponse struct {
	Ready               bool                `json:"ready"`
	Mode                string              `json:"mode"`
	EffectiveRouting    bool                `json:"effective_routing_enabled"`
	ConfiguredBackends  int                 `json:"configured_backends"`
	InitializedBackends int                 `json:"initialized_backends"`
	FailedBackends      int                 `json:"failed_backends"`
	Backends            []debugBackendEntry `json:"backends"`
}

type debugBackendEntry struct {
	Name         string             `json:"name"`
	Type         string             `json:"type"`
	DefaultModel string             `json:"default_model,omitempty"`
	Initialized  bool               `json:"initialized"`
	InitError    string             `json:"init_error,omitempty"`
	Stats        routeStatsSnapshot `json:"stats"`
}

type routeStatsSnapshot struct {
	EWMATTFTms    float64 `json:"ewma_ttft_ms"`
	Samples       int64   `json:"samples"`
	Total         int64   `json:"total"`
	Errors        int64   `json:"errors"`
	ErrorRate     float64 `json:"error_rate"`
	CooldownUntil string  `json:"cooldown_until,omitempty"`
	LastStatus    string  `json:"last_status,omitempty"`
	LastTTFTms    int64   `json:"last_ttft_ms"`
	LastAt        string  `json:"last_at,omitempty"`
}

type sanitizedConfig struct {
	ServerHost              string                     `json:"server_host"`
	ServerPort              int                        `json:"server_port"`
	ServerShutdownGraceSecs int                        `json:"server_shutdown_grace_seconds"`
	ServerDebugExpose       bool                       `json:"server_debug_expose"`
	Backend                 string                     `json:"backend,omitempty"`
	Routing                 sanitizedRoutingConfig     `json:"routing"`
	Backends                []sanitizedBackendInstance `json:"backends,omitempty"`
	OpenAI                  sanitizedOpenAIConfig      `json:"openai"`
	Vertex                  config.VertexConfig        `json:"vertex"`
}

type sanitizedRoutingConfig struct {
	Enabled         bool    `json:"enabled"`
	Policy          string  `json:"policy"`
	CooldownSeconds int     `json:"cooldown_seconds"`
	EwmaAlpha       float64 `json:"ewma_alpha"`
	MinSamples      int     `json:"min_samples"`
	Prefer          string  `json:"prefer,omitempty"`
}

type sanitizedBackendInstance struct {
	Name   string                `json:"name"`
	Type   string                `json:"type"`
	Vertex config.VertexConfig   `json:"vertex"`
	OpenAI sanitizedOpenAIConfig `json:"openai"`
}

type sanitizedOpenAIConfig struct {
	APIKeyEnv      string            `json:"api_key_env"`
	BaseURL        string            `json:"base_url"`
	Model          string            `json:"model"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	ExtraHeaders   map[string]string `json:"extra_headers,omitempty"`
}

func (s *Server) debugRoutesHandler(w http.ResponseWriter, _ *http.Request) {
	stats := map[string]routing.StatsSnapshot(nil)
	if s.router != nil {
		stats = s.router.Snapshot()
	}

	backends := make([]debugBackendEntry, 0, len(s.runtime.Backends))
	for _, backend := range s.runtime.Backends {
		backends = append(backends, debugBackendEntry{
			Name:         backend.Name,
			Type:         backend.Type,
			DefaultModel: backend.DefaultModel,
			Initialized:  backend.Initialized,
			InitError:    backend.InitError,
			Stats:        marshalStatsSnapshot(stats[backend.Name]),
		})
	}

	_ = writeJSON(w, http.StatusOK, debugRoutesResponse{
		Ready:               s.runtime.Ready(),
		Mode:                s.runtime.Mode(),
		EffectiveRouting:    s.runtime.RoutingEnabled,
		ConfiguredBackends:  s.runtime.ConfiguredBackends(),
		InitializedBackends: s.runtime.InitializedBackends(),
		FailedBackends:      s.runtime.FailedBackends(),
		Backends:            backends,
	})
}

func (s *Server) debugConfigHandler(w http.ResponseWriter, _ *http.Request) {
	if s.cfg == nil {
		_ = writeJSON(w, http.StatusOK, sanitizedConfig{})
		return
	}

	backends := make([]sanitizedBackendInstance, 0, len(s.cfg.Backends))
	for _, backend := range s.cfg.Backends {
		backends = append(backends, sanitizedBackendInstance{
			Name:   backend.Name,
			Type:   backend.Type,
			Vertex: backend.Vertex,
			OpenAI: sanitizeOpenAIConfig(backend.OpenAI),
		})
	}

	_ = writeJSON(w, http.StatusOK, sanitizedConfig{
		ServerHost:              s.cfg.Host,
		ServerPort:              s.cfg.Port,
		ServerShutdownGraceSecs: s.cfg.ShutdownGraceSeconds,
		ServerDebugExpose:       s.cfg.DebugExpose,
		Backend:                 s.cfg.Backend,
		Routing:                 sanitizeRoutingConfig(s.cfg.Routing),
		Backends:                backends,
		OpenAI:                  sanitizeOpenAIConfig(s.cfg.OpenAI),
		Vertex:                  s.cfg.Vertex,
	})
}

func marshalStatsSnapshot(snapshot routing.StatsSnapshot) routeStatsSnapshot {
	out := routeStatsSnapshot{
		EWMATTFTms: snapshot.EWMATTFTms,
		Samples:    snapshot.Samples,
		Total:      snapshot.Total,
		Errors:     snapshot.Errors,
		ErrorRate:  snapshot.ErrorRate,
		LastStatus: snapshot.LastStatus,
		LastTTFTms: snapshot.LastTTFTms,
	}
	if !snapshot.CooldownUntil.IsZero() {
		out.CooldownUntil = snapshot.CooldownUntil.UTC().Format(http.TimeFormat)
	}
	if !snapshot.LastAt.IsZero() {
		out.LastAt = snapshot.LastAt.UTC().Format(http.TimeFormat)
	}

	return out
}

func sanitizeOpenAIConfig(cfg config.OpenAIConfig) sanitizedOpenAIConfig {
	return sanitizedOpenAIConfig{
		APIKeyEnv:      cfg.APIKeyEnv,
		BaseURL:        cfg.BaseURL,
		Model:          cfg.Model,
		TimeoutSeconds: cfg.TimeoutSeconds,
		ExtraHeaders:   maskHeaders(cfg.ExtraHeaders),
	}
}

func sanitizeRoutingConfig(cfg config.RoutingConfig) sanitizedRoutingConfig {
	return sanitizedRoutingConfig{
		Enabled:         cfg.Enabled,
		Policy:          cfg.Policy,
		CooldownSeconds: cfg.CooldownSeconds,
		EwmaAlpha:       cfg.EwmaAlpha,
		MinSamples:      cfg.MinSamples,
		Prefer:          cfg.Prefer,
	}
}

func maskHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	masked := make(map[string]string, len(headers))
	for key, value := range headers {
		if value == "" {
			masked[key] = ""
			continue
		}
		masked[key] = "<masked>"
	}

	return masked
}
