package config

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

func Validate(cfg Config) error {
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		return err
	}

	if cfg.Vertex.IsConfigured() && !cfg.Vertex.IsComplete() {
		missing := cfg.Vertex.MissingFields()
		return fmt.Errorf(
			"vertex configuration is incomplete: set %s when any vertex field is configured",
			strings.Join(missing, ", "),
		)
	}

	return nil
}
