package routing

import (
	"math"
	"slices"
	"sort"
	"time"
)

type PolicyConfig struct {
	Enabled         bool
	Policy          string
	CooldownSeconds int
	EwmaAlpha       float64
	MinSamples      int64
	Prefer          string
}

type Backend struct {
	Name   string
	Type   string
	Client Streamer
}

type CandidateDecision struct {
	Name     string
	Type     string
	Model    string
	Stats    StatsSnapshot
	Score    float64
	Eligible bool
	Notes    string
}

type Decision struct {
	Chosen     Backend
	Reason     string
	Candidates []CandidateDecision
}

type Router struct {
	backends []Backend
	stats    *StatsStore
	cfg      PolicyConfig
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

	return &Router{
		backends: cloned,
		stats:    stats,
		cfg:      cfg,
	}
}

func (r *Router) Choose(modelOverride string) Decision {
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
	candidates := make([]CandidateDecision, 0, len(r.backends))
	eligibleIdx := make([]int, 0, len(r.backends))
	hasCooldown := false

	for _, backend := range r.backends {
		stats := StatsSnapshot{}
		if r.stats != nil {
			stats = r.stats.Get(backend.Name).Snapshot(now)
		}

		score := math.MaxFloat64
		eligible := !stats.InCooldown
		notes := "insufficient_samples"
		if stats.InCooldown {
			hasCooldown = true
			notes = "cooldown"
		} else if stats.Samples >= r.cfg.MinSamples && stats.EWMATTFTms > 0 {
			score = stats.EWMATTFTms
			notes = "ewma_ready"
		}

		candidates = append(candidates, CandidateDecision{
			Name:     backend.Name,
			Type:     backend.Type,
			Model:    backend.Client.ResolvedModel(modelOverride),
			Stats:    stats,
			Score:    score,
			Eligible: eligible,
			Notes:    notes,
		})
		if eligible {
			eligibleIdx = append(eligibleIdx, len(candidates)-1)
		}
	}

	if len(eligibleIdx) == 0 {
		chosenIdx := r.pickEarliestCooldown(candidates)
		return Decision{
			Chosen:     r.backends[chosenIdx],
			Reason:     "all_in_cooldown",
			Candidates: candidates,
		}
	}

	if len(eligibleIdx) == 1 && hasCooldown {
		chosenIdx := eligibleIdx[0]
		return Decision{
			Chosen:     r.backends[chosenIdx],
			Reason:     "fallback_only_healthy",
			Candidates: candidates,
		}
	}

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
		return Decision{
			Chosen:     r.backends[bestIdx],
			Reason:     "lowest_ewma_ttft",
			Candidates: candidates,
		}
	}

	chosenIdx := r.pickNoStats(candidates, eligibleIdx)
	return Decision{
		Chosen:     r.backends[chosenIdx],
		Reason:     "no_stats_yet",
		Candidates: candidates,
	}
}

func (r *Router) Observe(backendName string, status string, ttftMs int64, providerError bool) {
	r.ObserveWithCooldown(backendName, status, ttftMs, providerError, 0)
}

func (r *Router) ObserveWithCooldown(backendName string, status string, ttftMs int64, providerError bool, cooldownOverride time.Duration) {
	if r == nil || r.stats == nil || backendName == "" {
		return
	}

	r.stats.ObserveWithCooldown(backendName, time.Now(), status, ttftMs, providerError, cooldownOverride)
}

func (r *Router) Snapshot() map[string]StatsSnapshot {
	if r == nil || r.stats == nil {
		return nil
	}

	return r.stats.Snapshot(time.Now())
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

func (r *Router) pickEarliestCooldown(candidates []CandidateDecision) int {
	best := 0
	for idx := 1; idx < len(candidates); idx++ {
		if candidates[idx].Stats.CooldownUntil.Before(candidates[best].Stats.CooldownUntil) {
			best = idx
			continue
		}
		if candidates[idx].Stats.CooldownUntil.Equal(candidates[best].Stats.CooldownUntil) && r.tieBreak(candidates[idx], candidates[best]) {
			best = idx
		}
	}

	return best
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
