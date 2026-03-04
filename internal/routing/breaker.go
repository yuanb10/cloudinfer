package routing

import (
	"sync"
	"time"
)

const (
	defaultBreakerConsecutiveFailures = 3
	defaultBreakerWindowSize          = 5
	defaultBreakerFailureRate         = 0.6
	defaultHalfOpenProbeInterval      = 1 * time.Second
)

type BreakerState string

const (
	BreakerStateClosed   BreakerState = "closed"
	BreakerStateOpen     BreakerState = "open"
	BreakerStateHalfOpen BreakerState = "half_open"
)

type BreakerConfig struct {
	ConsecutiveFailures   int
	WindowSize            int
	FailureRateThreshold  float64
	HalfOpenProbeInterval time.Duration
}

type BreakerOutcome int

const (
	BreakerOutcomeNeutral BreakerOutcome = iota
	BreakerOutcomeSuccess
	BreakerOutcomeFailure
)

type BreakerTransition struct {
	From BreakerState
	To   BreakerState
}

type BreakerSnapshot struct {
	State               BreakerState `json:"state"`
	OpenUntil           time.Time    `json:"open_until,omitempty"`
	LastTransitionAt    time.Time    `json:"last_transition_at,omitempty"`
	LastErrorCategory   string       `json:"last_error_category,omitempty"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	WindowSize          int          `json:"window_size"`
	RecentRequests      int          `json:"recent_requests"`
	RecentFailures      int          `json:"recent_failures"`
	FailureRate         float64      `json:"failure_rate"`
	ProbeAllowed        bool         `json:"probe_allowed"`
	ProbeInFlight       bool         `json:"probe_in_flight"`
}

type Breaker struct {
	mu                sync.Mutex
	cfg               BreakerConfig
	state             BreakerState
	openUntil         time.Time
	lastTransitionAt  time.Time
	lastErrorCategory string
	consecutiveFails  int
	recent            []bool
	probeInFlight     bool
	lastProbeAt       time.Time
}

func NewBreaker(cfg BreakerConfig) *Breaker {
	cfg = cfg.Normalize()
	return &Breaker{
		cfg:   cfg,
		state: BreakerStateClosed,
	}
}

func (c BreakerConfig) Normalize() BreakerConfig {
	if c.ConsecutiveFailures <= 0 {
		c.ConsecutiveFailures = defaultBreakerConsecutiveFailures
	}
	if c.WindowSize <= 0 {
		c.WindowSize = defaultBreakerWindowSize
	}
	if c.FailureRateThreshold <= 0 || c.FailureRateThreshold > 1 {
		c.FailureRateThreshold = defaultBreakerFailureRate
	}
	if c.HalfOpenProbeInterval <= 0 {
		c.HalfOpenProbeInterval = defaultHalfOpenProbeInterval
	}
	return c
}

func (b *Breaker) Snapshot(now time.Time) BreakerSnapshot {
	if b == nil {
		return BreakerSnapshot{State: BreakerStateClosed}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.advanceLocked(now)
	return b.snapshotLocked(now)
}

func (b *Breaker) TryAcquireProbe(now time.Time) bool {
	if b == nil {
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.advanceLocked(now)
	if b.state != BreakerStateHalfOpen {
		return b.state == BreakerStateClosed
	}
	if b.probeInFlight {
		return false
	}
	if !b.lastProbeAt.IsZero() && now.Sub(b.lastProbeAt) < b.cfg.HalfOpenProbeInterval {
		return false
	}

	b.probeInFlight = true
	b.lastProbeAt = now
	return true
}

func (b *Breaker) Observe(now time.Time, outcome BreakerOutcome, category string, cooldown time.Duration) BreakerTransition {
	if b == nil {
		return BreakerTransition{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.advanceLocked(now)

	switch outcome {
	case BreakerOutcomeSuccess:
		if b.state == BreakerStateHalfOpen {
			return b.transitionLocked(BreakerStateClosed, now)
		}
		b.probeInFlight = false
		b.consecutiveFails = 0
		b.recordLocked(false)
	case BreakerOutcomeFailure:
		b.lastErrorCategory = category
		b.recordLocked(true)

		if b.state == BreakerStateHalfOpen {
			b.probeInFlight = false
			return b.reopenLocked(now, cooldown)
		}

		b.consecutiveFails++
		if b.shouldOpenLocked() {
			return b.reopenLocked(now, cooldown)
		}
	case BreakerOutcomeNeutral:
		if b.state == BreakerStateHalfOpen {
			b.probeInFlight = false
			b.lastProbeAt = now
		}
	}

	return BreakerTransition{}
}

func (b *Breaker) advanceLocked(now time.Time) {
	if b.state == BreakerStateOpen && !b.openUntil.IsZero() && !now.Before(b.openUntil) {
		b.transitionLocked(BreakerStateHalfOpen, now)
	}
}

func (b *Breaker) shouldOpenLocked() bool {
	if b.consecutiveFails >= b.cfg.ConsecutiveFailures {
		return true
	}
	if len(b.recent) < b.cfg.WindowSize {
		return false
	}

	failures := 0
	for _, failed := range b.recent {
		if failed {
			failures++
		}
	}
	return float64(failures)/float64(len(b.recent)) >= b.cfg.FailureRateThreshold
}

func (b *Breaker) reopenLocked(now time.Time, cooldown time.Duration) BreakerTransition {
	if cooldown < 0 {
		cooldown = 0
	}
	b.openUntil = now.Add(cooldown)
	b.probeInFlight = false
	return b.transitionLocked(BreakerStateOpen, now)
}

func (b *Breaker) transitionLocked(next BreakerState, now time.Time) BreakerTransition {
	if b.state == next {
		return BreakerTransition{}
	}

	prev := b.state
	b.state = next
	b.lastTransitionAt = now

	switch next {
	case BreakerStateClosed:
		b.openUntil = time.Time{}
		b.consecutiveFails = 0
		b.probeInFlight = false
		b.lastProbeAt = time.Time{}
		b.recent = b.recent[:0]
	case BreakerStateOpen:
		b.probeInFlight = false
	case BreakerStateHalfOpen:
		b.probeInFlight = false
	}

	return BreakerTransition{From: prev, To: next}
}

func (b *Breaker) recordLocked(failed bool) {
	if b.cfg.WindowSize <= 0 {
		return
	}
	if len(b.recent) == b.cfg.WindowSize {
		copy(b.recent, b.recent[1:])
		b.recent[len(b.recent)-1] = failed
		return
	}

	b.recent = append(b.recent, failed)
}

func (b *Breaker) snapshotLocked(now time.Time) BreakerSnapshot {
	failures := 0
	for _, failed := range b.recent {
		if failed {
			failures++
		}
	}

	failureRate := 0.0
	if len(b.recent) > 0 {
		failureRate = float64(failures) / float64(len(b.recent))
	}

	probeAllowed := false
	if b.state == BreakerStateHalfOpen && !b.probeInFlight {
		probeAllowed = b.lastProbeAt.IsZero() || now.Sub(b.lastProbeAt) >= b.cfg.HalfOpenProbeInterval
	}

	return BreakerSnapshot{
		State:               b.state,
		OpenUntil:           b.openUntil,
		LastTransitionAt:    b.lastTransitionAt,
		LastErrorCategory:   b.lastErrorCategory,
		ConsecutiveFailures: b.consecutiveFails,
		WindowSize:          b.cfg.WindowSize,
		RecentRequests:      len(b.recent),
		RecentFailures:      failures,
		FailureRate:         failureRate,
		ProbeAllowed:        probeAllowed,
		ProbeInFlight:       b.probeInFlight,
	}
}
