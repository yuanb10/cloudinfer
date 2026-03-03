package wrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/routing"
)

func TestAdapterStreamerNoRetryAfterFirstTokenFailure(t *testing.T) {
	testErr := errors.New("mid-stream failure")
	fake := &countingAdapter{
		stream: &scriptedStream{
			events: []streamEvent{
				{delta: adapters.Delta{Text: "a"}},
				{err: testErr},
			},
		},
	}

	streamer := NewAdapterStreamer("alpha", "test-model", fake)
	tokenCh, errCh := streamer.StreamText(context.Background(), "default", []routing.Message{
		{Role: "user", Content: "hello"},
	})

	select {
	case token := <-tokenCh:
		if token != "a" {
			t.Fatalf("first token = %q, want %q", token, "a")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first token")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, testErr) {
			t.Fatalf("stream error = %v, want %v", err, testErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal error")
	}

	if fake.calls != 1 {
		t.Fatalf("Generate() calls = %d, want %d", fake.calls, 1)
	}
}

type countingAdapter struct {
	calls  int
	stream adapters.Stream
	err    error
}

func (a *countingAdapter) Name() string {
	return "counting"
}

func (a *countingAdapter) Generate(context.Context, adapters.NormalizedRequest) (adapters.Stream, adapters.Meta, error) {
	a.calls++
	if a.err != nil {
		return nil, adapters.Meta{}, a.err
	}
	return a.stream, adapters.Meta{Model: "test-model", StreamMode: adapters.StreamModeStream}, nil
}

type scriptedStream struct {
	events []streamEvent
	index  int
}

type streamEvent struct {
	delta adapters.Delta
	err   error
}

func (s *scriptedStream) Recv() (adapters.Delta, error) {
	if s.index >= len(s.events) {
		return adapters.Delta{}, errors.New("unexpected extra recv")
	}

	event := s.events[s.index]
	s.index++
	return event.delta, event.err
}

func (s *scriptedStream) Close() error {
	return nil
}
