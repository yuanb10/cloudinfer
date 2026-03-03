package config

import "testing"

func TestValidateRejectsDebugExposeWithoutAuthTokenEnv(t *testing.T) {
	cfg := Config{
		ServerConfig: ServerConfig{
			Host:                 "127.0.0.1",
			Port:                 8080,
			ShutdownGraceSeconds: 20,
			DebugExpose:          true,
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got, want := err.Error(), "server_debug_expose requires server_debug_auth_token_env"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestValidateAllowsDebugExposeWithAuthTokenEnv(t *testing.T) {
	cfg := Config{
		ServerConfig: ServerConfig{
			Host:                 "127.0.0.1",
			Port:                 8080,
			ShutdownGraceSeconds: 20,
			DebugExpose:          true,
			DebugAuthTokenEnv:    "CLOUDINFER_DEBUG_TOKEN",
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

func TestValidateRejectsInvalidRoutingJitter(t *testing.T) {
	cfg := Config{
		ServerConfig: ServerConfig{
			Host:                 "127.0.0.1",
			Port:                 8080,
			ShutdownGraceSeconds: 20,
		},
		Routing: RoutingConfig{
			CooldownJitterFraction: 1.5,
		},
	}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got, want := err.Error(), "routing.cooldown_jitter_fraction must be between 0 and 1"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
