package routing

import (
	"context"
	"time"
)

const (
	defaultTotalTimeout = 30 * time.Second
	defaultTTFTTimeout  = 1500 * time.Millisecond
	defaultIdleTimeout  = 10 * time.Second
)

type TimeoutPolicy struct {
	Total time.Duration
	TTFT  time.Duration
	Idle  time.Duration
}

func (p TimeoutPolicy) Normalize() TimeoutPolicy {
	if p.Total <= 0 {
		p.Total = defaultTotalTimeout
	}
	if p.TTFT <= 0 {
		p.TTFT = defaultTTFTTimeout
	}
	if p.Idle <= 0 {
		p.Idle = defaultIdleTimeout
	}
	return p
}

type DeadlineBudget struct {
	deadline time.Time
}

func NewDeadlineBudget(now time.Time, total time.Duration) DeadlineBudget {
	if total <= 0 {
		return DeadlineBudget{}
	}

	return DeadlineBudget{deadline: now.Add(total)}
}

func BudgetFromContext(ctx context.Context) DeadlineBudget {
	if ctx == nil {
		return DeadlineBudget{}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return DeadlineBudget{}
	}

	return DeadlineBudget{deadline: deadline}
}

func (b DeadlineBudget) Deadline() (time.Time, bool) {
	if b.deadline.IsZero() {
		return time.Time{}, false
	}

	return b.deadline, true
}

func (b DeadlineBudget) Remaining(now time.Time) time.Duration {
	if b.deadline.IsZero() {
		return 0
	}

	remaining := time.Until(b.deadline)
	if !now.IsZero() {
		remaining = b.deadline.Sub(now)
	}
	if remaining < 0 {
		return 0
	}

	return remaining
}

func (b DeadlineBudget) PhaseTimeout(now time.Time, phase time.Duration) time.Duration {
	if phase <= 0 {
		return 0
	}

	remaining := b.Remaining(now)
	if remaining <= 0 {
		return 0
	}
	if remaining < phase {
		return remaining
	}

	return phase
}

func (b DeadlineBudget) WithContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := b.Deadline(); ok {
		return context.WithDeadline(parent, deadline)
	}

	return context.WithCancel(parent)
}
