package config

import (
	"fmt"
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

	return nil
}
