package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	ServerConfig `mapstructure:",squash"`
	Vertex       VertexConfig `mapstructure:"vertex" yaml:"vertex"`
}

type ServerConfig struct {
	Host string `mapstructure:"server_host" validate:"required,hostname|ip"`
	Port int    `mapstructure:"server_port" validate:"required,min=1,max=65535"`
}

type VertexConfig struct {
	Project  string `mapstructure:"project" yaml:"project"`
	Location string `mapstructure:"location" yaml:"location"`
	Model    string `mapstructure:"model" yaml:"model"`
}

func (c ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func (c VertexConfig) IsConfigured() bool {
	return strings.TrimSpace(c.Project) != "" ||
		strings.TrimSpace(c.Location) != "" ||
		strings.TrimSpace(c.Model) != ""
}

func (c VertexConfig) IsComplete() bool {
	return strings.TrimSpace(c.Project) != "" &&
		strings.TrimSpace(c.Location) != "" &&
		strings.TrimSpace(c.Model) != ""
}

func (c VertexConfig) MissingFields() []string {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(c.Project) == "" {
		missing = append(missing, "vertex.project")
	}
	if strings.TrimSpace(c.Location) == "" {
		missing = append(missing, "vertex.location")
	}
	if strings.TrimSpace(c.Model) == "" {
		missing = append(missing, "vertex.model")
	}

	return missing
}

func Load(configPath string) (Config, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("router")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
	}

	v.SetDefault("server_host", "0.0.0.0")
	v.SetDefault("server_port", 8080)
	v.SetDefault("vertex.project", "")
	v.SetDefault("vertex.location", "")
	v.SetDefault("vertex.model", "")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}
