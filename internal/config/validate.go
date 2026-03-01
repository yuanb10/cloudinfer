package config

import "github.com/go-playground/validator/v10"

func Validate(cfg Config) error {
	validate := validator.New()
	return validate.Struct(cfg)
}
