package routing

import (
	"math"
	"sync"
	"time"
)

type StatsStore struct {
	mu             sync.RWMutex
	stats          map[string]*Stats
	alpha          float64
	cooldown       time.Duration
	jitterFraction float64
	jitterSource   func() float64
}

type Stats struct {
	mu            sync.RWMutex
	ewmaTTFT      float64
	samples       int64
	total         int64
	errors        int64
	cooldownUntil time.Time
	lastStatus    string
	lastError     string
	lastTTFTms    int64
	lastAt        time.Time
}

type StatsSnapshot struct {
	EWMATTFTms    float64
	Samples       int64
	Total         int64
	Errors        int64
	ErrorRate     float64
	CooldownUntil time.Time
	InCooldown    bool
	LastStatus    string
	LastError     string
	LastTTFTms    int64
	LastAt        time.Time
}

func NewStatsStore(alpha float64, cooldown time.Duration) *StatsStore {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	if cooldown < 0 {
		cooldown = 0
	}

	return &StatsStore{
		stats:    make(map[string]*Stats),
		alpha:    alpha,
		cooldown: cooldown,
	}
}

func (s *StatsStore) WithCooldownJitter(fraction float64, source func() float64) *StatsStore {
	if s == nil {
		return nil
	}

	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	if source == nil {
		source = func() float64 {
			return 0.5
		}
	}

	s.jitterFraction = fraction
	s.jitterSource = source
	return s
}

func (s *StatsStore) Get(name string) *Stats {
	s.mu.RLock()
	stat := s.stats[name]
	s.mu.RUnlock()
	if stat != nil {
		return stat
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if stat = s.stats[name]; stat != nil {
		return stat
	}

	stat = &Stats{}
	s.stats[name] = stat
	return stat
}

func (s *StatsStore) Observe(name string, now time.Time, status string, ttftMs int64, providerError bool) {
	s.ObserveWithCooldown(name, now, status, ttftMs, providerError, 0)
}

func (s *StatsStore) ObserveWithCooldown(name string, now time.Time, status string, ttftMs int64, providerError bool, cooldownOverride time.Duration) {
	if s == nil {
		return
	}

	cooldown := s.CooldownDuration(cooldownOverride)

	s.Get(name).Observe(now, status, ttftMs, providerError, s.alpha, cooldown)
}

func (s *StatsStore) CooldownDuration(cooldownOverride time.Duration) time.Duration {
	if s == nil {
		return 0
	}

	if cooldownOverride > 0 {
		return cooldownOverride
	}

	return applyDurationJitter(s.cooldown, s.jitterFraction, s.jitterSource)
}

func (s *StatsStore) Snapshot(now time.Time) map[string]StatsSnapshot {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	names := make([]string, 0, len(s.stats))
	for name := range s.stats {
		names = append(names, name)
	}
	s.mu.RUnlock()

	snapshots := make(map[string]StatsSnapshot, len(names))
	for _, name := range names {
		snapshots[name] = s.Get(name).Snapshot(now)
	}

	return snapshots
}

func (s *Stats) Observe(now time.Time, status string, ttftMs int64, providerError bool, alpha float64, cooldown time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.total++
	s.lastStatus = status
	s.lastTTFTms = ttftMs
	s.lastAt = now

	if ttftMs > 0 {
		value := float64(ttftMs)
		if s.samples == 0 || s.ewmaTTFT == 0 {
			s.ewmaTTFT = value
		} else {
			s.ewmaTTFT = (alpha * value) + ((1 - alpha) * s.ewmaTTFT)
		}
		s.samples++
	}

	if providerError {
		s.errors++
		s.lastError = status
		s.cooldownUntil = now.Add(cooldown)
	}
}

func applyDurationJitter(base time.Duration, fraction float64, source func() float64) time.Duration {
	if base <= 0 || fraction <= 0 || source == nil {
		return base
	}

	sample := source()
	if sample < 0 {
		sample = 0
	}
	if sample > 1 {
		sample = 1
	}

	multiplier := 1 + ((sample*2)-1)*fraction
	if multiplier < 0 {
		multiplier = 0
	}

	jittered := time.Duration(math.Round(float64(base) * multiplier))
	if jittered < 0 {
		return 0
	}

	return jittered
}

func (s *Stats) Snapshot(now time.Time) StatsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	errorRate := 0.0
	if s.total > 0 {
		errorRate = float64(s.errors) / float64(s.total)
	}

	return StatsSnapshot{
		EWMATTFTms:    s.ewmaTTFT,
		Samples:       s.samples,
		Total:         s.total,
		Errors:        s.errors,
		ErrorRate:     errorRate,
		CooldownUntil: s.cooldownUntil,
		InCooldown:    !s.cooldownUntil.IsZero() && now.Before(s.cooldownUntil),
		LastStatus:    s.lastStatus,
		LastError:     s.lastError,
		LastTTFTms:    s.lastTTFTms,
		LastAt:        s.lastAt,
	}
}
