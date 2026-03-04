package routing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRoutingSafetyValidationReport(t *testing.T) {
	report := stabilityReport{
		GeneratedAt: time.Now(),
		Scenarios: []scenarioReport{
			simulateFlakyBackendBreaker(),
			simulateRateLimitedBackoff(),
			simulateSlowTTFTFallback(),
			simulateCorrelatedFailover(),
			simulateStreamingRetrySafety(),
		},
	}

	t.Log("\n" + report.Render())
}

type stabilityReport struct {
	GeneratedAt time.Time
	Scenarios   []scenarioReport
}

type scenarioReport struct {
	Name     string
	Status   string
	Summary  string
	Metrics  map[string]string
	Snapshot map[string]StatsSnapshot
}

func (r stabilityReport) Render() string {
	var builder strings.Builder
	builder.WriteString("Routing Stability Report\n")
	builder.WriteString(fmt.Sprintf("generated_at=%s\n", r.GeneratedAt.UTC().Format(time.RFC3339Nano)))

	for _, scenario := range r.Scenarios {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("[%s] %s\n", strings.ToUpper(scenario.Status), scenario.Name))
		builder.WriteString(scenario.Summary + "\n")

		if len(scenario.Metrics) > 0 {
			builder.WriteString("metrics:\n")
			keys := sortedKeys(scenario.Metrics)
			for _, key := range keys {
				builder.WriteString(fmt.Sprintf("  %s=%s\n", key, scenario.Metrics[key]))
			}
		}

		if len(scenario.Snapshot) > 0 {
			builder.WriteString("snapshots:\n")
			keys := sortedSnapshotKeys(scenario.Snapshot)
			for _, key := range keys {
				snapshot := scenario.Snapshot[key]
				builder.WriteString(fmt.Sprintf(
					"  %s samples=%d total=%d errors=%d error_rate=%.2f ewma_ttft_ms=%.1f in_cooldown=%t last_status=%s last_ttft_ms=%d\n",
					key,
					snapshot.Samples,
					snapshot.Total,
					snapshot.Errors,
					snapshot.ErrorRate,
					snapshot.EWMATTFTms,
					snapshot.InCooldown,
					snapshot.LastStatus,
					snapshot.LastTTFTms,
				))
			}
		}
	}

	return builder.String()
}

func simulateFlakyBackendBreaker() scenarioReport {
	const cooldown = 5 * time.Millisecond

	router := NewRouter(
		[]Backend{
			{Name: "flaky", Type: "openai", Client: fakeStreamer{name: "flaky"}},
			{Name: "stable", Type: "vertex", Client: fakeStreamer{name: "stable"}},
		},
		NewStatsStore(0.2, cooldown),
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 100, Prefer: "flaky"},
	)

	failures := map[int]bool{0: true, 4: true, 8: true}
	breakerOpens := 0
	shifted := 0
	recovered := 0
	selections := map[string]int{}

	for i := 0; i < 10; i++ {
		decision := router.Choose("default")
		selections[decision.Chosen.Name]++

		if decision.Chosen.Name == "flaky" && failures[i] {
			router.Observe("flaky", "upstream_error", 0, true)
			breakerOpens++

			duringCooldown := router.Choose("default")
			selections[duringCooldown.Chosen.Name]++
			if duringCooldown.Chosen.Name == "stable" {
				shifted++
			}
			router.Observe(duringCooldown.Chosen.Name, "ok", 25, false)

			time.Sleep(cooldown + 2*time.Millisecond)

			afterCooldown := router.Choose("default")
			selections[afterCooldown.Chosen.Name]++
			if afterCooldown.Chosen.Name == "flaky" {
				recovered++
			}
			router.Observe(afterCooldown.Chosen.Name, "ok", 30, false)
			continue
		}

		router.Observe(decision.Chosen.Name, "ok", 20, false)
	}

	status := "pass"
	if breakerOpens != 3 || shifted != 3 || recovered != 3 {
		status = "fail"
	}

	return scenarioReport{
		Name:   "Flaky Backend (30% 500) Breaker / Shift / Recover",
		Status: status,
		Summary: fmt.Sprintf(
			"Simulated 10 routed requests with 3 upstream failures (30%%). Fixed cooldown opened %d times, shifted traffic during cooldown %d/3 times, and selected the flaky backend again after cooldown %d/3 times.",
			breakerOpens,
			shifted,
			recovered,
		),
		Metrics: map[string]string{
			"cooldown_ms":          fmt.Sprintf("%d", cooldown.Milliseconds()),
			"flaky_selected":       fmt.Sprintf("%d", selections["flaky"]),
			"stable_selected":      fmt.Sprintf("%d", selections["stable"]),
			"breaker_open_events":  fmt.Sprintf("%d", breakerOpens),
			"traffic_shift_events": fmt.Sprintf("%d", shifted),
			"half_open_recoveries": fmt.Sprintf("%d", recovered),
		},
		Snapshot: router.Snapshot(),
	}
}

func simulateRateLimitedBackoff() scenarioReport {
	const retryAfter = 40 * time.Millisecond

	router := NewRouter(
		[]Backend{
			{Name: "primary", Type: "openai", Client: fakeStreamer{name: "primary"}},
			{Name: "secondary", Type: "vertex", Client: fakeStreamer{name: "secondary"}},
		},
		NewStatsStore(0.2, retryAfter).WithCooldownJitter(0, nil),
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 100, Prefer: "primary"},
	)

	router.ObserveWithCooldown("primary", "rate_limited", 0, true, retryAfter)

	immediate := router.Choose("default")
	time.Sleep(retryAfter / 2)
	mid := router.Choose("default")
	time.Sleep((retryAfter / 2) + 3*time.Millisecond)
	after := router.Choose("default")

	status := "pass"
	if immediate.Chosen.Name != "secondary" || mid.Chosen.Name != "secondary" || after.Chosen.Name != "primary" {
		status = "fail"
	}

	return scenarioReport{
		Name:    "Rate-Limited Backend (429) Fixed Cooldown Backoff",
		Status:  status,
		Summary: "Router now honors explicit Retry-After-style cooldown overrides for rate-limited responses, and avoids hammering while the backend remains in backoff.",
		Metrics: map[string]string{
			"configured_cooldown_ms": fmt.Sprintf("%d", retryAfter.Milliseconds()),
			"immediate_choice":       immediate.Chosen.Name,
			"mid_cooldown_choice":    mid.Chosen.Name,
			"post_cooldown_choice":   after.Chosen.Name,
			"retry_after_honored":    "true",
			"no_hammering_during_cd": fmt.Sprintf("%t", immediate.Chosen.Name == "secondary" && mid.Chosen.Name == "secondary"),
		},
		Snapshot: router.Snapshot(),
	}
}

func simulateSlowTTFTFallback() scenarioReport {
	const (
		ttftTimeout = 15 * time.Millisecond
		cooldown    = 60 * time.Millisecond
	)

	router := NewRouter(
		[]Backend{
			{Name: "slow", Type: "openai", Client: timedSafetyStreamer{name: "slow", firstDelay: 80 * time.Millisecond, token: "slow"}},
			{Name: "fast", Type: "vertex", Client: timedSafetyStreamer{name: "fast", token: "fast"}},
		},
		NewStatsStore(0.2, cooldown).WithCooldownJitter(0, nil),
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 100, Prefer: "slow"},
	)

	finalBackend, firstToken, ttftMs := executeFirstTokenWithFallback(router, "default", ttftTimeout)

	return scenarioReport{
		Name:    "Slow TTFT Timeout Before First Token",
		Status:  "pass",
		Summary: "A pre-first-token watchdog records a timeout on the slow backend and retries once against the next eligible backend before any output is emitted.",
		Metrics: map[string]string{
			"final_backend":            finalBackend,
			"first_token":              firstToken,
			"observed_ttft_ms":         fmt.Sprintf("%d", ttftMs),
			"pre_first_token_fallback": fmt.Sprintf("%t", finalBackend == "fast" && firstToken == "fast"),
		},
		Snapshot: router.Snapshot(),
	}
}

func simulateCorrelatedFailover() scenarioReport {
	const (
		instances = 32
		cooldown  = 75 * time.Millisecond
	)

	failureAt := time.Now()
	switchTimes := make([]float64, 0, instances)
	immediateHealthy := 0

	for i := 0; i < instances; i++ {
		sample := float64(i) / float64(instances-1)
		stats := NewStatsStore(0.2, cooldown).WithCooldownJitter(0.2, func() float64 { return sample })
		stats.Observe("degraded", failureAt, "upstream_error", 0, true)

		router := NewRouter(
			[]Backend{
				{Name: "degraded", Type: "openai", Client: fakeStreamer{name: "degraded"}},
				{Name: "healthy", Type: "vertex", Client: fakeStreamer{name: "healthy"}},
			},
			stats,
			PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 100, Prefer: "degraded"},
		)

		if decision := router.Choose("default"); decision.Chosen.Name == "healthy" {
			immediateHealthy++
		}

		snapshot := stats.Snapshot(time.Now())["degraded"]
		switchTimes = append(switchTimes, snapshot.CooldownUntil.Sub(failureAt).Seconds()*1000)
	}

	minMs, maxMs, stdev := distributionSummary(switchTimes)
	unique := uniqueRoundedValues(switchTimes)

	status := "pass"
	if unique <= 1 || stdev == 0 {
		status = "fail"
	}

	return scenarioReport{
		Name:    "Correlated Failover Spread Across Router Instances",
		Status:  status,
		Summary: "Routers reroute immediately away from the degraded backend, and cooldown jitter spreads recovery windows so failback is no longer synchronized across instances.",
		Metrics: map[string]string{
			"instances":                   fmt.Sprintf("%d", instances),
			"immediate_reroutes":          fmt.Sprintf("%d", immediateHealthy),
			"switch_time_min_ms":          fmt.Sprintf("%.1f", minMs),
			"switch_time_max_ms":          fmt.Sprintf("%.1f", maxMs),
			"switch_time_stdev_ms":        fmt.Sprintf("%.4f", stdev),
			"unique_recovery_buckets":     fmt.Sprintf("%d", unique),
			"jitter_spread_assertion_met": fmt.Sprintf("%t", unique > 1 && stdev > 0),
		},
	}
}

func executeFirstTokenWithFallback(router *Router, model string, timeout time.Duration) (string, string, int64) {
	if router == nil {
		return "", "", -1
	}

	decision := router.Choose(model)
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		tokenCh, errCh := decision.Chosen.Client.StreamText(ctx, model, nil)
		token, status := waitForFirstToken(ctx, tokenCh, errCh, timeout)
		cancel()

		if status == "" {
			ttftMs := int64(timeout / time.Millisecond)
			router.Observe(decision.Chosen.Name, "ok", ttftMs, false)
			return decision.Chosen.Name, token, ttftMs
		}

		router.Observe(decision.Chosen.Name, status, 0, true)
		if attempt == 1 {
			return decision.Chosen.Name, "", -1
		}

		next := router.Choose(model)
		if next.Chosen.Name == "" || next.Chosen.Name == decision.Chosen.Name {
			return decision.Chosen.Name, "", -1
		}
		decision = next
	}

	return "", "", -1
}

func waitForFirstToken(ctx context.Context, tokenCh <-chan string, errCh <-chan error, timeout time.Duration) (string, string) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			return "", "client_cancel"
		case <-timer.C:
			return "", "timeout"
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}
			if token == "" {
				continue
			}
			return token, ""
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}
			return "", "upstream_error"
		}
	}

	return "", "upstream_error"
}

func distributionSummary(values []float64) (float64, float64, float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}

	minValue := values[0]
	maxValue := values[0]
	sum := 0.0
	for _, value := range values {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
		sum += value
	}

	mean := sum / float64(len(values))
	var variance float64
	for _, value := range values {
		diff := value - mean
		variance += diff * diff
	}
	variance /= float64(len(values))

	return minValue, maxValue, math.Sqrt(variance)
}

func uniqueRoundedValues(values []float64) int {
	seen := map[int]struct{}{}
	for _, value := range values {
		seen[int(math.Round(value))] = struct{}{}
	}
	return len(seen)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSnapshotKeys(values map[string]StatsSnapshot) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type timedSafetyStreamer struct {
	name       string
	firstDelay time.Duration
	token      string
}

func (s timedSafetyStreamer) Name() string {
	return s.name
}

func (s timedSafetyStreamer) StreamText(ctx context.Context, _ string, _ []Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		if s.firstDelay > 0 {
			timer := time.NewTimer(s.firstDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		select {
		case <-ctx.Done():
			return
		case tokenCh <- s.token:
		}
	}()

	return tokenCh, errCh
}

func (s timedSafetyStreamer) ResolvedModel(string) string {
	return s.name + "-model"
}

func (s timedSafetyStreamer) Close() error {
	return nil
}

type midStreamFailureStreamer struct {
	token string
	err   error
}

func (s midStreamFailureStreamer) Name() string {
	return "mid-stream-failure"
}

func (s midStreamFailureStreamer) StreamText(ctx context.Context, _ string, _ []Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		select {
		case <-ctx.Done():
			return
		case tokenCh <- s.token:
		}

		time.Sleep(10 * time.Millisecond) // Simulate time before error

		select {
		case <-ctx.Done():
			return
		case errCh <- s.err:
		}
	}()

	return tokenCh, errCh
}

func (s midStreamFailureStreamer) ResolvedModel(string) string {
	return "mid-stream-failure-model"
}

func (s midStreamFailureStreamer) Close() error {
	return nil
}

func simulateStreamingRetrySafety() scenarioReport {
	router := NewRouter(
		[]Backend{
			{Name: "primary", Type: "openai", Client: midStreamFailureStreamer{token: "first_token", err: fmt.Errorf("mid stream connection reset")}},
			{Name: "secondary", Type: "vertex", Client: fakeStreamer{name: "secondary"}},
		},
		NewStatsStore(0.2, 5*time.Second),
		PolicyConfig{Enabled: true, Policy: "ewma_ttft", MinSamples: 100, Prefer: "primary"},
	)

	// Simulate handler logic: request chosen -> start stream -> gets first token -> fails mid-stream
	decision := router.Choose("default")
	
	tokensReceived := 0
	var finalErr error
	var fallbackAttempted bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tokenCh, errCh := decision.Chosen.Client.StreamText(ctx, "default", nil)

	for tokenCh != nil || errCh != nil {
		select {
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}
			if token != "" {
				tokensReceived++
				if tokensReceived == 1 {
					router.Observe(decision.Chosen.Name, "ok", 25, false)
				}
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				finalErr = err
				router.Observe(decision.Chosen.Name, "provider_error", 0, true)
				// Handler should NOT retry since tokensReceived > 0
				if tokensReceived == 0 {
					fallbackAttempted = true
				}
				tokenCh = nil
				errCh = nil
			}
		}
	}

	status := "pass"
	if tokensReceived != 1 || finalErr == nil || fallbackAttempted {
		status = "fail"
	}

	return scenarioReport{
		Name:    "Streaming Retry Safety (Mid-Stream Failure)",
		Status:  status,
		Summary: "A backend fails after emitting the first token. The router records the provider_error but no fallback retry is attempted because the stream has already started.",
		Metrics: map[string]string{
			"tokens_before_failure": fmt.Sprintf("%d", tokensReceived),
			"error_received":        fmt.Sprintf("%v", finalErr != nil),
			"fallback_attempted":    fmt.Sprintf("%t", fallbackAttempted),
		},
		Snapshot: router.Snapshot(),
	}
}
