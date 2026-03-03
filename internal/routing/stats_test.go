package routing

import (
	"testing"
	"time"
)

func TestStatsStoreObserveWithCooldownOverrideHonorsExplicitBackoff(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStatsStore(0.2, 10*time.Second).WithCooldownJitter(0.5, func() float64 { return 1.0 })

	store.ObserveWithCooldown("alpha", now, "rate_limited", 0, true, 3*time.Second)

	snapshot := store.Snapshot(now)["alpha"]
	if got, want := snapshot.CooldownUntil, now.Add(3*time.Second); !got.Equal(want) {
		t.Fatalf("cooldown_until = %s, want %s", got, want)
	}
}

func TestStatsStoreObserveAppliesJitterToDefaultCooldown(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStatsStore(0.2, 10*time.Second).WithCooldownJitter(0.2, func() float64 { return 1.0 })

	store.Observe("alpha", now, "upstream_error", 0, true)

	snapshot := store.Snapshot(now)["alpha"]
	if got, want := snapshot.CooldownUntil, now.Add(12*time.Second); !got.Equal(want) {
		t.Fatalf("cooldown_until = %s, want %s", got, want)
	}
}
