package routing

import (
	"sync"
	"time"
)

type StatsStore struct {
	mu       sync.RWMutex
	stats    map[string]*Stats
	alpha    float64
	cooldown time.Duration
}

type Stats struct {
	mu            sync.RWMutex
	ewmaTTFT      float64
	samples       int64
	total         int64
	errors        int64
	cooldownUntil time.Time
	lastStatus    string
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
	if s == nil {
		return
	}

	s.Get(name).Observe(now, status, ttftMs, providerError, s.alpha, s.cooldown)
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
		s.cooldownUntil = now.Add(cooldown)
	}
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
		LastTTFTms:    s.lastTTFTms,
		LastAt:        s.lastAt,
	}
}
