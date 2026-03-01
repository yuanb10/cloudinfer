package config

import (
	"errors"

	"github.com/go-playground/validator/v10"
)

func Validate(cfg Config) error {
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		return err
	}

	if cfg.Vertex.IsConfigured() && !cfg.Vertex.IsComplete() {
		return errors.New("vertex project, location, and model must all be set when vertex is configured")
	}

	return nil
}
