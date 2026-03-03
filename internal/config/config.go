package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	ServerConfig `mapstructure:",squash"`
	Backend      string            `mapstructure:"backend" yaml:"backend"`
	Routing      RoutingConfig     `mapstructure:"routing" yaml:"routing"`
	Backends     []BackendInstance `mapstructure:"backends" yaml:"backends"`
	OpenAI       OpenAIConfig      `mapstructure:"openai" yaml:"openai"`
	Vertex       VertexConfig      `mapstructure:"vertex" yaml:"vertex"`
}

type ServerConfig struct {
	Host                 string `mapstructure:"server_host" validate:"required,hostname|ip"`
	Port                 int    `mapstructure:"server_port" validate:"required,min=1,max=65535"`
	ShutdownGraceSeconds int    `mapstructure:"server_shutdown_grace_seconds" validate:"required,min=1,max=600"`
	DebugExpose          bool   `mapstructure:"server_debug_expose" yaml:"server_debug_expose"`
}

type VertexConfig struct {
	Project  string `mapstructure:"project" yaml:"project"`
	Location string `mapstructure:"location" yaml:"location"`
	Model    string `mapstructure:"model" yaml:"model"`
}

type RoutingConfig struct {
	Enabled         bool    `mapstructure:"enabled" yaml:"enabled"`
	EnabledSet      bool    `mapstructure:"-" yaml:"-"`
	Policy          string  `mapstructure:"policy" yaml:"policy"`
	CooldownSeconds int     `mapstructure:"cooldown_seconds" yaml:"cooldown_seconds"`
	EwmaAlpha       float64 `mapstructure:"ewma_alpha" yaml:"ewma_alpha"`
	MinSamples      int     `mapstructure:"min_samples" yaml:"min_samples"`
	Prefer          string  `mapstructure:"prefer" yaml:"prefer"`
}

type BackendInstance struct {
	Name   string       `mapstructure:"name" yaml:"name"`
	Type   string       `mapstructure:"type" yaml:"type"`
	Vertex VertexConfig `mapstructure:"vertex" yaml:"vertex"`
	OpenAI OpenAIConfig `mapstructure:"openai" yaml:"openai"`
}

type OpenAIConfig struct {
	APIKeyEnv      string            `mapstructure:"api_key_env" yaml:"api_key_env"`
	BaseURL        string            `mapstructure:"base_url" yaml:"base_url"`
	Model          string            `mapstructure:"model" yaml:"model"`
	TimeoutSeconds int               `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	ExtraHeaders   map[string]string `mapstructure:"extra_headers" yaml:"extra_headers"`
}

func (c ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func (c ServerConfig) ShutdownGracePeriod() time.Duration {
	return time.Duration(c.ShutdownGraceSeconds) * time.Second
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
	v.SetDefault("server_shutdown_grace_seconds", 20)
	v.SetDefault("server_debug_expose", false)
	v.SetDefault("backend", "")
	v.SetDefault("routing.policy", "ewma_ttft")
	v.SetDefault("routing.cooldown_seconds", 15)
	v.SetDefault("routing.ewma_alpha", 0.2)
	v.SetDefault("routing.min_samples", 5)
	v.SetDefault("routing.prefer", "")
	v.SetDefault("openai.api_key_env", "OPENAI_API_KEY")
	v.SetDefault("openai.base_url", "https://api.openai.com/v1")
	v.SetDefault("openai.model", "gpt-4o-mini")
	v.SetDefault("openai.timeout_seconds", 60)
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

	cfg.Routing.EnabledSet = v.IsSet("routing.enabled")
	cfg = synthesizeLegacyBackends(cfg)

	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func (c Config) EffectiveRoutingEnabled() bool {
	if c.Routing.EnabledSet {
		return c.Routing.Enabled
	}

	return len(c.Backends) >= 2
}

func synthesizeLegacyBackends(cfg Config) Config {
	if len(cfg.Backends) > 0 {
		return cfg
	}

	switch {
	case cfg.Backend == "openai":
		cfg.Backends = []BackendInstance{
			{
				Name:   "openai-default",
				Type:   "openai",
				OpenAI: cfg.OpenAI,
			},
		}
	case cfg.Backend == "vertex" || cfg.Vertex.IsConfigured():
		cfg.Backends = []BackendInstance{
			{
				Name:   "vertex-default",
				Type:   "vertex",
				Vertex: cfg.Vertex,
			},
		}
	}

	return cfg
}
