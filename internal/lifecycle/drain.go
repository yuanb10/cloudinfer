package lifecycle

import (
	"context"
	"sync/atomic"
	"time"
)

// DrainState tracks whether the server is draining and how many streams are in flight.
// It is intentionally small and lock-free to keep shutdown paths deterministic.
type DrainState struct {
	draining atomic.Bool
	inFlight atomic.Int64
	deadline atomic.Int64
}

func NewDrainState() *DrainState {
	return &DrainState{}
}

// TryAcquireStream increments the in-flight counter if the server is not already draining.
// It returns true when the caller may proceed to start streaming.
func (d *DrainState) TryAcquireStream() bool {
	if d.draining.Load() {
		return false
	}

	d.inFlight.Add(1)
	if d.draining.Load() {
		d.inFlight.Add(-1)
		return false
	}

	return true
}

// ReleaseStream decrements the in-flight counter, clamping at zero.
func (d *DrainState) ReleaseStream() {
	if d.inFlight.Add(-1) < 0 {
		d.inFlight.Store(0)
	}
}

// StartDrain marks the server as draining and records the absolute deadline.
func (d *DrainState) StartDrain(deadline time.Time) {
	d.deadline.Store(deadline.UnixNano())
	d.draining.Store(true)
}

func (d *DrainState) Draining() bool {
	return d.draining.Load()
}

func (d *DrainState) InFlight() int64 {
	return d.inFlight.Load()
}

func (d *DrainState) DrainDeadline() time.Time {
	if ts := d.deadline.Load(); ts != 0 {
		return time.Unix(0, ts)
	}
	return time.Time{}
}

// Wait blocks until all in-flight streams complete or the context is canceled.
// It returns true when drained before the context deadline.
func (d *DrainState) Wait(ctx context.Context) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if d.InFlight() == 0 {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
