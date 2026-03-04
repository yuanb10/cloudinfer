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
	Current             debugCurrentRoute   `json:"current_route"`
	Backends            []debugBackendEntry `json:"backends"`
}

type debugCurrentRoute struct {
	Backend string `json:"backend,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type debugBackendEntry struct {
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	DefaultModel string               `json:"default_model,omitempty"`
	Initialized  bool                 `json:"initialized"`
	InitError    string               `json:"init_error,omitempty"`
	Health       string               `json:"health"`
	RouteReason  string               `json:"route_reason,omitempty"`
	Stats        routeStatsSnapshot   `json:"stats"`
	Breaker      routeBreakerSnapshot `json:"breaker"`
}

type routeStatsSnapshot struct {
	EWMATTFTms        float64  `json:"ewma_ttft_ms"`
	RouteScoreMs      *float64 `json:"route_score_ms,omitempty"`
	Samples           int64    `json:"samples"`
	Total             int64    `json:"total"`
	Errors            int64    `json:"errors"`
	ErrorRate         float64  `json:"error_rate"`
	CooldownUntil     string   `json:"cooldown_until,omitempty"`
	LastStatus        string   `json:"last_status,omitempty"`
	LastErrorCategory string   `json:"last_error_category,omitempty"`
	LastTTFTms        int64    `json:"last_ttft_ms"`
	LastAt            string   `json:"last_at,omitempty"`
}

type routeBreakerSnapshot struct {
	State               string  `json:"state"`
	OpenUntil           string  `json:"open_until,omitempty"`
	LastTransitionAt    string  `json:"last_transition_at,omitempty"`
	LastErrorCategory   string  `json:"last_error_category,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	RecentRequests      int     `json:"recent_requests"`
	RecentFailures      int     `json:"recent_failures"`
	FailureRate         float64 `json:"failure_rate"`
	ProbeAllowed        bool    `json:"probe_allowed"`
	ProbeInFlight       bool    `json:"probe_in_flight"`
}

type sanitizedConfig struct {
	ServerHost              string                     `json:"server_host"`
	ServerPort              int                        `json:"server_port"`
	ServerShutdownGraceSecs int                        `json:"server_shutdown_grace_seconds"`
	ServerDebugExpose       bool                       `json:"server_debug_expose"`
	ServerDebugAuthTokenEnv string                     `json:"server_debug_auth_token_env"`
	Backend                 string                     `json:"backend,omitempty"`
	Routing                 sanitizedRoutingConfig     `json:"routing"`
	Backends                []sanitizedBackendInstance `json:"backends,omitempty"`
	OpenAI                  sanitizedOpenAIConfig      `json:"openai"`
	Vertex                  config.VertexConfig        `json:"vertex"`
}

type sanitizedRoutingConfig struct {
	Enabled                        bool    `json:"enabled"`
	Policy                         string  `json:"policy"`
	CooldownSeconds                int     `json:"cooldown_seconds"`
	CooldownJitterFraction         float64 `json:"cooldown_jitter_fraction"`
	TotalTimeoutMs                 int     `json:"total_timeout_ms"`
	TTFTTimeoutMs                  int     `json:"ttft_timeout_ms"`
	IdleTimeoutMs                  int     `json:"idle_timeout_ms"`
	EwmaAlpha                      float64 `json:"ewma_alpha"`
	MinSamples                     int     `json:"min_samples"`
	Prefer                         string  `json:"prefer,omitempty"`
	BreakerConsecutiveFailures     int     `json:"breaker_consecutive_failures"`
	BreakerWindowSize              int     `json:"breaker_window_size"`
	BreakerFailureRate             float64 `json:"breaker_failure_rate"`
	BreakerHalfOpenProbeIntervalMs int     `json:"breaker_half_open_probe_interval_ms"`
	RetryMaxAttempts               int     `json:"retry_max_attempts"`
	RetryBaseBackoffMs             int     `json:"retry_base_backoff_ms"`
	RetryMaxBackoffMs              int     `json:"retry_max_backoff_ms"`
	RetryJitterFraction            float64 `json:"retry_jitter_fraction"`
}

type sanitizedBackendInstance struct {
	Name    string                        `json:"name"`
	Type    string                        `json:"type"`
	Routing sanitizedBackendRoutingConfig `json:"routing"`
	Vertex  config.VertexConfig           `json:"vertex"`
	OpenAI  sanitizedOpenAIConfig         `json:"openai"`
}

type sanitizedBackendRoutingConfig struct {
	TotalTimeoutMs                 *int     `json:"total_timeout_ms,omitempty"`
	TTFTTimeoutMs                  *int     `json:"ttft_timeout_ms,omitempty"`
	IdleTimeoutMs                  *int     `json:"idle_timeout_ms,omitempty"`
	BreakerConsecutiveFailures     *int     `json:"breaker_consecutive_failures,omitempty"`
	BreakerWindowSize              *int     `json:"breaker_window_size,omitempty"`
	BreakerFailureRate             *float64 `json:"breaker_failure_rate,omitempty"`
	BreakerHalfOpenProbeIntervalMs *int     `json:"breaker_half_open_probe_interval_ms,omitempty"`
	RetryMaxAttempts               *int     `json:"retry_max_attempts,omitempty"`
	RetryBaseBackoffMs             *int     `json:"retry_base_backoff_ms,omitempty"`
	RetryMaxBackoffMs              *int     `json:"retry_max_backoff_ms,omitempty"`
	RetryJitterFraction            *float64 `json:"retry_jitter_fraction,omitempty"`
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
	explanation := routing.Decision{}
	if s.router != nil {
		stats = s.router.Snapshot()
		explanation = s.router.Explain("default")
	}
	candidateByName := make(map[string]routing.CandidateDecision, len(explanation.Candidates))
	for _, candidate := range explanation.Candidates {
		candidateByName[candidate.Name] = candidate
	}

	backends := make([]debugBackendEntry, 0, len(s.runtime.Backends))
	for _, backend := range s.runtime.Backends {
		candidate := candidateByName[backend.Name]
		snapshot := stats[backend.Name]
		backends = append(backends, debugBackendEntry{
			Name:         backend.Name,
			Type:         backend.Type,
			DefaultModel: backend.DefaultModel,
			Initialized:  backend.Initialized,
			InitError:    backend.InitError,
			Health:       classifyBackendHealth(backend, snapshot, candidate.Breaker),
			RouteReason:  candidate.Notes,
			Stats:        marshalStatsSnapshot(snapshot, candidate.Score),
			Breaker:      marshalBreakerSnapshot(candidate.Breaker),
		})
	}

	_ = writeJSON(w, http.StatusOK, debugRoutesResponse{
		Ready:               s.runtime.Ready(),
		Mode:                s.runtime.Mode(),
		EffectiveRouting:    s.runtime.RoutingEnabled,
		ConfiguredBackends:  s.runtime.ConfiguredBackends(),
		InitializedBackends: s.runtime.InitializedBackends(),
		FailedBackends:      s.runtime.FailedBackends(),
		Current: debugCurrentRoute{
			Backend: explanation.Chosen.Name,
			Reason:  explanation.Reason,
		},
		Backends: backends,
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
			Name:    backend.Name,
			Type:    backend.Type,
			Routing: sanitizeBackendRoutingConfig(backend.Routing),
			Vertex:  backend.Vertex,
			OpenAI:  sanitizeOpenAIConfig(backend.OpenAI),
		})
	}

	_ = writeJSON(w, http.StatusOK, sanitizedConfig{
		ServerHost:              s.cfg.Host,
		ServerPort:              s.cfg.Port,
		ServerShutdownGraceSecs: s.cfg.ShutdownGraceSeconds,
		ServerDebugExpose:       s.cfg.DebugExpose,
		ServerDebugAuthTokenEnv: s.cfg.DebugAuthTokenEnv,
		Backend:                 s.cfg.Backend,
		Routing:                 sanitizeRoutingConfig(s.cfg.Routing),
		Backends:                backends,
		OpenAI:                  sanitizeOpenAIConfig(s.cfg.OpenAI),
		Vertex:                  s.cfg.Vertex,
	})
}

func marshalStatsSnapshot(snapshot routing.StatsSnapshot, score float64) routeStatsSnapshot {
	out := routeStatsSnapshot{
		EWMATTFTms:        snapshot.EWMATTFTms,
		Samples:           snapshot.Samples,
		Total:             snapshot.Total,
		Errors:            snapshot.Errors,
		ErrorRate:         snapshot.ErrorRate,
		LastStatus:        snapshot.LastStatus,
		LastErrorCategory: snapshot.LastError,
		LastTTFTms:        snapshot.LastTTFTms,
	}
	if score < 1e308 {
		out.RouteScoreMs = &score
	}
	if !snapshot.CooldownUntil.IsZero() {
		out.CooldownUntil = snapshot.CooldownUntil.UTC().Format(http.TimeFormat)
	}
	if !snapshot.LastAt.IsZero() {
		out.LastAt = snapshot.LastAt.UTC().Format(http.TimeFormat)
	}

	return out
}

func marshalBreakerSnapshot(snapshot routing.BreakerSnapshot) routeBreakerSnapshot {
	out := routeBreakerSnapshot{
		State:               string(snapshot.State),
		LastErrorCategory:   snapshot.LastErrorCategory,
		ConsecutiveFailures: snapshot.ConsecutiveFailures,
		RecentRequests:      snapshot.RecentRequests,
		RecentFailures:      snapshot.RecentFailures,
		FailureRate:         snapshot.FailureRate,
		ProbeAllowed:        snapshot.ProbeAllowed,
		ProbeInFlight:       snapshot.ProbeInFlight,
	}
	if !snapshot.OpenUntil.IsZero() {
		out.OpenUntil = snapshot.OpenUntil.UTC().Format(http.TimeFormat)
	}
	if !snapshot.LastTransitionAt.IsZero() {
		out.LastTransitionAt = snapshot.LastTransitionAt.UTC().Format(http.TimeFormat)
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
		Enabled:                        cfg.Enabled,
		Policy:                         cfg.Policy,
		CooldownSeconds:                cfg.CooldownSeconds,
		CooldownJitterFraction:         cfg.CooldownJitterFraction,
		TotalTimeoutMs:                 cfg.TotalTimeoutMs,
		TTFTTimeoutMs:                  cfg.TTFTTimeoutMs,
		IdleTimeoutMs:                  cfg.IdleTimeoutMs,
		EwmaAlpha:                      cfg.EwmaAlpha,
		MinSamples:                     cfg.MinSamples,
		Prefer:                         cfg.Prefer,
		BreakerConsecutiveFailures:     cfg.BreakerConsecutiveFailures,
		BreakerWindowSize:              cfg.BreakerWindowSize,
		BreakerFailureRate:             cfg.BreakerFailureRate,
		BreakerHalfOpenProbeIntervalMs: cfg.BreakerHalfOpenProbeIntervalMs,
		RetryMaxAttempts:               cfg.RetryMaxAttempts,
		RetryBaseBackoffMs:             cfg.RetryBaseBackoffMs,
		RetryMaxBackoffMs:              cfg.RetryMaxBackoffMs,
		RetryJitterFraction:            cfg.RetryJitterFraction,
	}
}

func sanitizeBackendRoutingConfig(cfg config.BackendRoutingConfig) sanitizedBackendRoutingConfig {
	return sanitizedBackendRoutingConfig{
		TotalTimeoutMs:                 cfg.TotalTimeoutMs,
		TTFTTimeoutMs:                  cfg.TTFTTimeoutMs,
		IdleTimeoutMs:                  cfg.IdleTimeoutMs,
		BreakerConsecutiveFailures:     cfg.BreakerConsecutiveFailures,
		BreakerWindowSize:              cfg.BreakerWindowSize,
		BreakerFailureRate:             cfg.BreakerFailureRate,
		BreakerHalfOpenProbeIntervalMs: cfg.BreakerHalfOpenProbeIntervalMs,
		RetryMaxAttempts:               cfg.RetryMaxAttempts,
		RetryBaseBackoffMs:             cfg.RetryBaseBackoffMs,
		RetryMaxBackoffMs:              cfg.RetryMaxBackoffMs,
		RetryJitterFraction:            cfg.RetryJitterFraction,
	}
}

func classifyBackendHealth(backend BackendStatus, stats routing.StatsSnapshot, breaker routing.BreakerSnapshot) string {
	if !backend.Initialized {
		return "unavailable"
	}
	if breaker.State == routing.BreakerStateOpen {
		return "tripped"
	}
	if breaker.State == routing.BreakerStateHalfOpen {
		return "probing"
	}
	if stats.InCooldown {
		return "cooldown"
	}
	if stats.LastStatus != "" && stats.LastStatus != "ok" && stats.LastStatus != "client_cancel" {
		return "degraded"
	}

	return "healthy"
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
