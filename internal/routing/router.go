package routing

import (
	"math"
	"slices"
	"sort"
	"sync"
	"time"
)

type PolicyConfig struct {
	Enabled         bool
	Policy          string
	CooldownSeconds int
	EwmaAlpha       float64
	MinSamples      int64
	Prefer          string
	Breaker         BreakerConfig
	Retry           RetryPolicy
	Timeouts        TimeoutPolicy
}

type BackendPolicy struct {
	Breaker  BreakerConfig
	Retry    RetryPolicy
	Timeouts TimeoutPolicy
}

type Backend struct {
	Name   string
	Type   string
	Client Streamer
	Policy BackendPolicy
}

type CandidateDecision struct {
	Name     string
	Type     string
	Model    string
	Stats    StatsSnapshot
	Breaker  BreakerSnapshot
	Score    float64
	Eligible bool
	Notes    string
}

type Decision struct {
	Chosen     Backend
	Reason     string
	Candidates []CandidateDecision
}

type ObservationResult struct {
	Cooldown          time.Duration
	BreakerTransition BreakerTransition
}

type Router struct {
	backends []Backend
	stats    *StatsStore
	cfg      PolicyConfig

	mu       sync.RWMutex
	breakers map[string]*Breaker
}

func NewRouter(backends []Backend, stats *StatsStore, cfg PolicyConfig) *Router {
	cloned := append([]Backend(nil), backends...)
	sort.Slice(cloned, func(i, j int) bool {
		return cloned[i].Name < cloned[j].Name
	})

	if cfg.Policy == "" {
		cfg.Policy = "ewma_ttft"
	}
	if cfg.EwmaAlpha <= 0 || cfg.EwmaAlpha > 1 {
		cfg.EwmaAlpha = 0.2
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 5
	}
	if cfg.CooldownSeconds < 0 {
		cfg.CooldownSeconds = 0
	}
	cfg.Breaker = cfg.Breaker.Normalize()
	cfg.Retry = cfg.Retry.Normalize()
	cfg.Timeouts = cfg.Timeouts.Normalize()

	breakers := make(map[string]*Breaker, len(cloned))
	for idx, backend := range cloned {
		cloned[idx].Policy = cfg.resolveBackendPolicy(backend.Policy)
		breakers[backend.Name] = NewBreaker(cloned[idx].Policy.Breaker)
	}

	return &Router{
		backends: cloned,
		stats:    stats,
		cfg:      cfg,
		breakers: breakers,
	}
}

func (r *Router) Choose(modelOverride string) Decision {
	return r.choose(modelOverride, true)
}

func (r *Router) Explain(modelOverride string) Decision {
	return r.choose(modelOverride, false)
}

func (r *Router) choose(modelOverride string, reserveHalfOpen bool) Decision {
	if r == nil || len(r.backends) == 0 {
		return Decision{}
	}

	if !r.cfg.Enabled {
		idx := r.pickDisabled()
		candidates := make([]CandidateDecision, 0, len(r.backends))
		now := time.Now()
		for _, backend := range r.backends {
			stats := StatsSnapshot{}
			if r.stats != nil {
				stats = r.stats.Get(backend.Name).Snapshot(now)
			}
			candidates = append(candidates, CandidateDecision{
				Name:     backend.Name,
				Type:     backend.Type,
				Model:    backend.Client.ResolvedModel(modelOverride),
				Stats:    stats,
				Breaker:  r.breakerSnapshot(backend.Name, now),
				Score:    math.MaxFloat64,
				Eligible: true,
				Notes:    "routing_disabled",
			})
		}

		return Decision{
			Chosen:     r.backends[idx],
			Reason:     "routing_disabled",
			Candidates: candidates,
		}
	}

	now := time.Now()
	candidates, eligibleIdx, hasGuardrails := r.buildCandidates(modelOverride, now)
	if len(eligibleIdx) == 0 {
		chosenIdx := r.pickMostRecoverable(candidates)

		reason := "all_unavailable"
		allCooldown := len(candidates) > 0
		for _, c := range candidates {
			if c.Notes != "cooldown" {
				allCooldown = false
				break
			}
		}
		if allCooldown {
			reason = "all_in_cooldown"
		}

		return Decision{
			Chosen:     r.backends[chosenIdx],
			Reason:     reason,
			Candidates: candidates,
		}
	}

	if len(eligibleIdx) == 1 && hasGuardrails {
		chosenIdx := eligibleIdx[0]
		if reserveHalfOpen && candidates[chosenIdx].Breaker.State == BreakerStateHalfOpen && !r.tryReserveProbe(candidates[chosenIdx].Name, now) {
			candidates[chosenIdx].Eligible = false
			candidates[chosenIdx].Notes = "half_open_probe_busy"
			chosenIdx = r.pickMostRecoverable(candidates)
			return Decision{
				Chosen:     r.backends[chosenIdx],
				Reason:     "all_unavailable",
				Candidates: candidates,
			}
		}
		if candidates[chosenIdx].Breaker.State == BreakerStateHalfOpen {
			candidates[chosenIdx].Notes = "half_open_probe"
		}
		return Decision{
			Chosen:     r.backends[chosenIdx],
			Reason:     "fallback_only_healthy",
			Candidates: candidates,
		}
	}

	for len(eligibleIdx) > 0 {
		chosenIdx := r.pickBestCandidate(candidates, eligibleIdx)
		if chosenIdx < 0 {
			break
		}

		if reserveHalfOpen && candidates[chosenIdx].Breaker.State == BreakerStateHalfOpen {
			if !r.tryReserveProbe(candidates[chosenIdx].Name, now) {
				candidates[chosenIdx].Eligible = false
				candidates[chosenIdx].Notes = "half_open_probe_busy"
				eligibleIdx = removeIndex(eligibleIdx, chosenIdx)
				continue
			}
			candidates[chosenIdx].Notes = "half_open_probe"
		}

		reason := "no_stats_yet"
		if candidates[chosenIdx].Score < math.MaxFloat64 {
			reason = "lowest_ewma_ttft"
		}

		return Decision{
			Chosen:     r.backends[chosenIdx],
			Reason:     reason,
			Candidates: candidates,
		}
	}

	chosenIdx := r.pickMostRecoverable(candidates)
	return Decision{
		Chosen:     r.backends[chosenIdx],
		Reason:     "all_unavailable",
		Candidates: candidates,
	}
}

func (r *Router) buildCandidates(modelOverride string, now time.Time) ([]CandidateDecision, []int, bool) {
	candidates := make([]CandidateDecision, 0, len(r.backends))
	eligibleIdx := make([]int, 0, len(r.backends))
	hasGuardrails := false

	for _, backend := range r.backends {
		stats := StatsSnapshot{}
		if r.stats != nil {
			stats = r.stats.Get(backend.Name).Snapshot(now)
		}
		breaker := r.breakerSnapshot(backend.Name, now)

		score := math.MaxFloat64
		eligible := true
		notes := "insufficient_samples"

		switch {
		case breaker.State == BreakerStateOpen:
			hasGuardrails = true
			eligible = false
			notes = "breaker_open"
		case breaker.State == BreakerStateHalfOpen && !breaker.ProbeAllowed:
			hasGuardrails = true
			eligible = false
			if breaker.ProbeInFlight {
				notes = "half_open_probe_busy"
			} else {
				notes = "half_open_probe_rate_limited"
			}
		case breaker.State == BreakerStateHalfOpen:
			hasGuardrails = true
			notes = "half_open_probe_ready"
		case stats.InCooldown:
			hasGuardrails = true
			eligible = false
			notes = "cooldown"
		case stats.Samples >= r.cfg.MinSamples && stats.EWMATTFTms > 0:
			score = stats.EWMATTFTms
			notes = "ewma_ready"
		}

		candidates = append(candidates, CandidateDecision{
			Name:     backend.Name,
			Type:     backend.Type,
			Model:    backend.Client.ResolvedModel(modelOverride),
			Stats:    stats,
			Breaker:  breaker,
			Score:    score,
			Eligible: eligible,
			Notes:    notes,
		})
		if eligible {
			eligibleIdx = append(eligibleIdx, len(candidates)-1)
		}
	}

	return candidates, eligibleIdx, hasGuardrails
}

func (c PolicyConfig) resolveBackendPolicy(policy BackendPolicy) BackendPolicy {
	if isZeroBreakerConfig(policy.Breaker) {
		policy.Breaker = c.Breaker
	} else {
		policy.Breaker = policy.Breaker.Normalize()
	}
	if isZeroRetryPolicy(policy.Retry) {
		policy.Retry = c.Retry
	} else {
		policy.Retry = policy.Retry.Normalize()
	}
	if isZeroTimeoutPolicy(policy.Timeouts) {
		policy.Timeouts = c.Timeouts
	} else {
		policy.Timeouts = policy.Timeouts.Normalize()
	}

	return policy
}

func isZeroBreakerConfig(cfg BreakerConfig) bool {
	return cfg.ConsecutiveFailures == 0 &&
		cfg.WindowSize == 0 &&
		cfg.FailureRateThreshold == 0 &&
		cfg.HalfOpenProbeInterval == 0
}

func isZeroRetryPolicy(policy RetryPolicy) bool {
	return policy.MaxAttempts == 0 &&
		policy.BaseBackoff == 0 &&
		policy.MaxBackoff == 0 &&
		policy.JitterFraction == 0 &&
		policy.JitterSource == nil
}

func isZeroTimeoutPolicy(policy TimeoutPolicy) bool {
	return policy.Total == 0 && policy.TTFT == 0 && policy.Idle == 0
}

func (r *Router) pickBestCandidate(candidates []CandidateDecision, eligibleIdx []int) int {
	bestIdx := -1
	bestScore := math.MaxFloat64
	for _, idx := range eligibleIdx {
		candidate := candidates[idx]
		if bestIdx == -1 {
			bestIdx = idx
			bestScore = candidate.Score
			continue
		}
		if candidate.Score < bestScore {
			bestScore = candidate.Score
			bestIdx = idx
			continue
		}
		if candidate.Score == bestScore && r.tieBreak(candidates[idx], candidates[bestIdx]) {
			bestIdx = idx
		}
	}

	if bestIdx >= 0 && bestScore < math.MaxFloat64 {
		return bestIdx
	}

	if bestIdx >= 0 {
		return r.pickNoStats(candidates, eligibleIdx)
	}

	return -1
}

func (r *Router) Observe(backendName string, status string, ttftMs int64, providerError bool) {
	r.ObserveWithCooldown(backendName, status, ttftMs, providerError, 0)
}

func (r *Router) ObserveResult(backendName string, status string, ttftMs int64, providerError bool) ObservationResult {
	return r.ObserveWithCooldownResult(backendName, status, ttftMs, providerError, 0)
}

func (r *Router) ObserveWithCooldown(backendName string, status string, ttftMs int64, providerError bool, cooldownOverride time.Duration) {
	r.ObserveWithCooldownResult(backendName, status, ttftMs, providerError, cooldownOverride)
}

func (r *Router) ObserveWithCooldownResult(backendName string, status string, ttftMs int64, providerError bool, cooldownOverride time.Duration) ObservationResult {
	if r == nil || backendName == "" {
		return ObservationResult{}
	}

	cooldown := cooldownOverride
	if r.stats != nil {
		cooldown = r.stats.CooldownDuration(cooldownOverride)
		r.stats.Get(backendName).Observe(time.Now(), status, ttftMs, providerError, r.cfg.EwmaAlpha, cooldown)
	}

	breaker := r.breaker(backendName)
	if breaker == nil {
		return ObservationResult{Cooldown: cooldown}
	}

	outcome := BreakerOutcomeNeutral
	switch {
	case providerError:
		outcome = BreakerOutcomeFailure
	case status == "ok":
		outcome = BreakerOutcomeSuccess
	}

	return ObservationResult{
		Cooldown:          cooldown,
		BreakerTransition: breaker.Observe(time.Now(), outcome, status, cooldown),
	}
}

func (r *Router) Snapshot() map[string]StatsSnapshot {
	if r == nil || r.stats == nil {
		return nil
	}

	return r.stats.Snapshot(time.Now())
}

func (r *Router) BreakerSnapshot() map[string]BreakerSnapshot {
	if r == nil {
		return nil
	}

	now := time.Now()
	out := make(map[string]BreakerSnapshot, len(r.backends))
	for _, backend := range r.backends {
		out[backend.Name] = r.breakerSnapshot(backend.Name, now)
	}

	return out
}

func (r *Router) RetryPolicy() RetryPolicy {
	if r == nil {
		return RetryPolicy{}.Normalize()
	}
	if len(r.backends) > 0 {
		return r.backends[0].Policy.Retry.Normalize()
	}

	return r.cfg.Retry.Normalize()
}

func (r *Router) TimeoutPolicy() TimeoutPolicy {
	if r == nil {
		return TimeoutPolicy{}.Normalize()
	}
	if len(r.backends) > 0 {
		return r.backends[0].Policy.Timeouts.Normalize()
	}

	return r.cfg.Timeouts.Normalize()
}

func (r *Router) RetryPolicyFor(backendName string) RetryPolicy {
	if backend, ok := r.BackendByName(backendName); ok {
		return backend.Policy.Retry.Normalize()
	}

	return r.RetryPolicy()
}

func (r *Router) TimeoutPolicyFor(backendName string) TimeoutPolicy {
	if backend, ok := r.BackendByName(backendName); ok {
		return backend.Policy.Timeouts.Normalize()
	}

	return r.TimeoutPolicy()
}

func (r *Router) Enabled() bool {
	return r != nil && r.cfg.Enabled
}

func (r *Router) HasBackends() bool {
	return r != nil && len(r.backends) > 0
}

func (r *Router) Backends() []Backend {
	if r == nil {
		return nil
	}

	return append([]Backend(nil), r.backends...)
}

func (r *Router) pickMostRecoverable(candidates []CandidateDecision) int {
	best := 0
	bestAt := recoveryAt(candidates[0])
	for idx := 1; idx < len(candidates); idx++ {
		candidateAt := recoveryAt(candidates[idx])
		if candidateAt.Before(bestAt) {
			best = idx
			bestAt = candidateAt
			continue
		}
		if candidateAt.Equal(bestAt) && r.tieBreak(candidates[idx], candidates[best]) {
			best = idx
			bestAt = candidateAt
		}
	}

	return best
}

func recoveryAt(candidate CandidateDecision) time.Time {
	if candidate.Breaker.State == BreakerStateOpen {
		return candidate.Breaker.OpenUntil
	}
	if !candidate.Stats.CooldownUntil.IsZero() {
		return candidate.Stats.CooldownUntil
	}
	return time.Time{}
}

func (r *Router) pickNoStats(candidates []CandidateDecision, eligibleIdx []int) int {
	if r.cfg.Prefer != "" {
		for _, idx := range eligibleIdx {
			if candidates[idx].Name == r.cfg.Prefer {
				return idx
			}
		}
	}

	sort.Slice(eligibleIdx, func(i, j int) bool {
		return candidates[eligibleIdx[i]].Name < candidates[eligibleIdx[j]].Name
	})
	return eligibleIdx[0]
}

func (r *Router) pickDisabled() int {
	if r.cfg.Prefer != "" {
		for idx, backend := range r.backends {
			if backend.Name == r.cfg.Prefer {
				return idx
			}
		}
	}

	return 0
}

func (r *Router) tieBreak(a CandidateDecision, b CandidateDecision) bool {
	if r.cfg.Prefer != "" {
		if a.Name == r.cfg.Prefer && b.Name != r.cfg.Prefer {
			return true
		}
		if b.Name == r.cfg.Prefer && a.Name != r.cfg.Prefer {
			return false
		}
	}

	if a.Breaker.State == BreakerStateClosed && b.Breaker.State != BreakerStateClosed {
		return true
	}
	if b.Breaker.State == BreakerStateClosed && a.Breaker.State != BreakerStateClosed {
		return false
	}

	return a.Name < b.Name
}

func (r *Router) BackendByName(name string) (Backend, bool) {
	if r == nil {
		return Backend{}, false
	}

	idx := slices.IndexFunc(r.backends, func(backend Backend) bool {
		return backend.Name == name
	})
	if idx < 0 {
		return Backend{}, false
	}

	return r.backends[idx], true
}

func (r *Router) breaker(name string) *Breaker {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	breaker := r.breakers[name]
	r.mu.RUnlock()
	if breaker != nil {
		return breaker
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if breaker = r.breakers[name]; breaker != nil {
		return breaker
	}

	if r.breakers == nil {
		r.breakers = make(map[string]*Breaker)
	}
	breaker = NewBreaker(r.cfg.Breaker)
	r.breakers[name] = breaker
	return breaker
}

func (r *Router) breakerSnapshot(name string, now time.Time) BreakerSnapshot {
	return r.breaker(name).Snapshot(now)
}

func (r *Router) tryReserveProbe(name string, now time.Time) bool {
	return r.breaker(name).TryAcquireProbe(now)
}

func removeIndex(values []int, target int) []int {
	out := values[:0]
	for _, value := range values {
		if value == target {
			continue
		}
		out = append(out, value)
	}
	return out
}
