package routing

import (
	"context"
	"testing"
	"time"
)

type fakeStreamer struct {
	name          string
	resolvedModel string
}

func (f fakeStreamer) Name() string {
	return f.name
}

func (f fakeStreamer) StreamText(context.Context, string, []Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error)
	close(tokenCh)
	close(errCh)
	return tokenCh, errCh
}

func (f fakeStreamer) ResolvedModel(string) string {
	if f.resolvedModel != "" {
		return f.resolvedModel
	}

	return "test-model"
}

func (f fakeStreamer) Close() error {
	return nil
}

func TestRouterChooseNoStatsPrefersConfiguredBackend(t *testing.T) {
	router := NewRouter(
		[]Backend{
			{Name: "b-backend", Type: "openai", Client: fakeStreamer{name: "b-backend"}},
			{Name: "a-backend", Type: "vertex", Client: fakeStreamer{name: "a-backend"}},
		},
		NewStatsStore(0.2, 15*time.Second),
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 5, Prefer: "b-backend"},
	)

	decision := router.Choose("default")
	if decision.Chosen.Name != "b-backend" {
		t.Fatalf("chosen = %q, want %q", decision.Chosen.Name, "b-backend")
	}
	if decision.Reason != "no_stats_yet" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "no_stats_yet")
	}
}

func TestRouterChooseFallbackOnlyHealthy(t *testing.T) {
	stats := NewStatsStore(0.2, 15*time.Second)
	now := time.Now()
	stats.Observe("alpha", now, "provider_error", 0, true)

	router := NewRouter(
		[]Backend{
			{Name: "alpha", Type: "openai", Client: fakeStreamer{name: "alpha"}},
			{Name: "beta", Type: "vertex", Client: fakeStreamer{name: "beta"}},
		},
		stats,
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 5},
	)

	decision := router.Choose("default")
	if decision.Chosen.Name != "beta" {
		t.Fatalf("chosen = %q, want %q", decision.Chosen.Name, "beta")
	}
	if decision.Reason != "fallback_only_healthy" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "fallback_only_healthy")
	}
}

func TestRouterChooseLowestEWMATTFT(t *testing.T) {
	stats := NewStatsStore(0.2, 15*time.Second)
	for i := 0; i < 5; i++ {
		stats.Observe("fast", time.Now(), "ok", 50, false)
		stats.Observe("slow", time.Now(), "ok", 200, false)
	}

	router := NewRouter(
		[]Backend{
			{Name: "fast", Type: "openai", Client: fakeStreamer{name: "fast"}},
			{Name: "slow", Type: "vertex", Client: fakeStreamer{name: "slow"}},
		},
		stats,
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 5},
	)

	decision := router.Choose("default")
	if decision.Chosen.Name != "fast" {
		t.Fatalf("chosen = %q, want %q", decision.Chosen.Name, "fast")
	}
	if decision.Reason != "lowest_ewma_ttft" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "lowest_ewma_ttft")
	}
}

func TestRouterChooseAllInCooldown(t *testing.T) {
	stats := NewStatsStore(0.2, 15*time.Second)
	earlier := time.Now()
	stats.Observe("earlier", earlier, "provider_error", 0, true)
	time.Sleep(10 * time.Millisecond)
	stats.Observe("later", time.Now(), "provider_error", 0, true)

	router := NewRouter(
		[]Backend{
			{Name: "earlier", Type: "openai", Client: fakeStreamer{name: "earlier"}},
			{Name: "later", Type: "vertex", Client: fakeStreamer{name: "later"}},
		},
		stats,
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 5},
	)

	decision := router.Choose("default")
	if decision.Chosen.Name != "earlier" {
		t.Fatalf("chosen = %q, want %q", decision.Chosen.Name, "earlier")
	}
	if decision.Reason != "all_in_cooldown" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "all_in_cooldown")
	}
}

func TestRouterChooseRespectsMinSamples(t *testing.T) {
	stats := NewStatsStore(0.2, 15*time.Second)
	for i := 0; i < 5; i++ {
		stats.Observe("established", time.Now(), "ok", 100, false)
	}
	stats.Observe("new", time.Now(), "ok", 10, false)

	router := NewRouter(
		[]Backend{
			{Name: "established", Type: "openai", Client: fakeStreamer{name: "established"}},
			{Name: "new", Type: "vertex", Client: fakeStreamer{name: "new"}},
		},
		stats,
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 5},
	)

	decision := router.Choose("default")
	if decision.Chosen.Name != "established" {
		t.Fatalf("chosen = %q, want %q", decision.Chosen.Name, "established")
	}
	if decision.Reason != "lowest_ewma_ttft" {
		t.Fatalf("reason = %q, want %q", decision.Reason, "lowest_ewma_ttft")
	}
}

func TestRouterExposesPerBackendPolicies(t *testing.T) {
	router := NewRouter(
		[]Backend{
			{
				Name:   "alpha",
				Type:   "openai",
				Client: fakeStreamer{name: "alpha"},
				Policy: BackendPolicy{
					Retry: RetryPolicy{MaxAttempts: 1},
					Timeouts: TimeoutPolicy{
						TTFT: 25 * time.Millisecond,
						Idle: 50 * time.Millisecond,
					},
				},
			},
			{Name: "beta", Type: "vertex", Client: fakeStreamer{name: "beta"}},
		},
		NewStatsStore(0.2, 15*time.Second),
		PolicyConfig{
			Enabled:    true,
			Policy:     "ewma_ttft",
			MinSamples: 5,
			Retry:      RetryPolicy{MaxAttempts: 3},
			Timeouts:   TimeoutPolicy{TTFT: 150 * time.Millisecond, Idle: 300 * time.Millisecond},
		},
	)

	if got, want := router.RetryPolicyFor("alpha").MaxAttempts, 1; got != want {
		t.Fatalf("alpha retry max_attempts = %d, want %d", got, want)
	}
	if got, want := router.RetryPolicyFor("beta").MaxAttempts, 3; got != want {
		t.Fatalf("beta retry max_attempts = %d, want %d", got, want)
	}
	if got, want := router.TimeoutPolicyFor("alpha").TTFT, 25*time.Millisecond; got != want {
		t.Fatalf("alpha ttft timeout = %s, want %s", got, want)
	}
	if got, want := router.TimeoutPolicyFor("beta").TTFT, 150*time.Millisecond; got != want {
		t.Fatalf("beta ttft timeout = %s, want %s", got, want)
	}
}
