package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestDrainStateTransitions(t *testing.T) {
	drain := NewDrainState()

	if drain.Draining() {
		t.Fatal("draining = true at start")
	}
	if drain.InFlight() != 0 {
		t.Fatalf("inFlight = %d, want 0", drain.InFlight())
	}

	if !drain.TryAcquireStream() {
		t.Fatal("expected to acquire stream before drain")
	}
	if drain.InFlight() != 1 {
		t.Fatalf("inFlight = %d, want 1 after acquire", drain.InFlight())
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	drain.StartDrain(deadline)

	if !drain.Draining() {
		t.Fatal("draining flag not set after StartDrain")
	}
	if drain.TryAcquireStream() {
		t.Fatal("expected TryAcquireStream to fail after drain started")
	}

	drain.ReleaseStream()
	if drain.InFlight() != 0 {
		t.Fatalf("inFlight = %d, want 0 after release", drain.InFlight())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if !drain.Wait(ctx) {
		t.Fatal("expected Wait to drain before context timeout")
	}

	gotDeadline := drain.DrainDeadline()
	if gotDeadline.IsZero() {
		t.Fatal("drain deadline is zero after StartDrain")
	}
	if diff := gotDeadline.Sub(deadline); diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("drain deadline mismatch: got %s want %s", gotDeadline, deadline)
	}
}
