package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-playground/validator/v10"
)

func Validate(cfg Config) error {
	validate := validator.New()
	if err := validate.Var(cfg.Backend, "omitempty,oneof=vertex openai"); err != nil {
		return fmt.Errorf("backend must be empty, \"vertex\", or \"openai\"")
	}

	if err := validate.Struct(cfg); err != nil {
		return err
	}

	if cfg.Backend == "vertex" && !cfg.Vertex.IsComplete() {
		missing := cfg.Vertex.MissingFields()
		return fmt.Errorf(
			"vertex backend requires %s",
			strings.Join(missing, ", "),
		)
	}

	if cfg.Vertex.IsConfigured() && !cfg.Vertex.IsComplete() {
		missing := cfg.Vertex.MissingFields()
		return fmt.Errorf(
			"vertex configuration is incomplete: set %s when any vertex field is configured",
			strings.Join(missing, ", "),
		)
	}

	if cfg.Backend == "openai" && strings.TrimSpace(cfg.OpenAI.APIKeyEnv) == "" {
		return fmt.Errorf("openai backend requires openai.api_key_env")
	}

	if cfg.DebugExpose && strings.TrimSpace(cfg.DebugAuthTokenEnv) == "" {
		return fmt.Errorf("server_debug_expose requires server_debug_auth_token_env")
	}

	if cfg.EffectiveRoutingEnabled() && len(cfg.Backends) < 2 {
		return fmt.Errorf("routing.enabled requires at least 2 backend instances")
	}

	if cfg.EffectiveRoutingEnabled() && cfg.Routing.Policy != "ewma_ttft" {
		return fmt.Errorf("routing.policy must be \"ewma_ttft\"")
	}
	if cfg.Routing.CooldownJitterFraction < 0 || cfg.Routing.CooldownJitterFraction > 1 {
		return fmt.Errorf("routing.cooldown_jitter_fraction must be between 0 and 1")
	}
	if cfg.Routing.TTFTTimeoutMs < 0 {
		return fmt.Errorf("routing.ttft_timeout_ms must be >= 0")
	}

	seen := make(map[string]struct{}, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		name := strings.TrimSpace(backend.Name)
		if name == "" {
			return fmt.Errorf("backend instance name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("backend instance names must be unique: %q", name)
		}
		seen[name] = struct{}{}

		switch strings.TrimSpace(backend.Type) {
		case "vertex":
			if !backend.Vertex.IsComplete() {
				return fmt.Errorf("vertex backend %q requires %s", name, strings.Join(backend.Vertex.MissingFields(), ", "))
			}
		case "openai":
			if strings.TrimSpace(backend.OpenAI.APIKeyEnv) == "" {
				return fmt.Errorf("openai backend %q requires openai.api_key_env", name)
			}
		default:
			return fmt.Errorf("backend %q type must be \"vertex\" or \"openai\"", name)
		}
	}

	if strings.TrimSpace(cfg.Routing.Prefer) != "" && !slices.ContainsFunc(cfg.Backends, func(backend BackendInstance) bool {
		return backend.Name == cfg.Routing.Prefer
	}) {
		return fmt.Errorf("routing.prefer must match a configured backend name")
	}

	return nil
}
