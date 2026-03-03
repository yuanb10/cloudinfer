package adapters

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type Adapter interface {
	Name() string
	Generate(ctx context.Context, req NormalizedRequest) (Stream, Meta, error)
}

type Stream interface {
	Recv() (Delta, error)
	Close() error
}

type NormalizedRequest struct {
	Model           string
	Input           []NormalizedInputItem
	Stream          bool
	MaxOutputTokens int
	Temperature     float64
}

type NormalizedInputItem struct {
	Role    string
	Content string
}

type Delta struct {
	Text string
}

type Meta struct {
	Model      string
	StreamMode string
}

const (
	StreamModeStream = "stream"
	StreamModeSingle = "single"
)

type ErrorCategory string

const (
	CategoryRateLimited    ErrorCategory = "rate_limited"
	CategoryTimeout        ErrorCategory = "timeout"
	CategoryAuthFailed     ErrorCategory = "auth_failed"
	CategoryUpstreamError  ErrorCategory = "upstream_error"
	CategoryInvalidRequest ErrorCategory = "invalid_request"
)

type Error struct {
	Category   ErrorCategory
	RetryAfter time.Duration
	StatusCode int
	Message    string
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}

	parts := []string{}
	if e.Category != "" {
		parts = append(parts, string(e.Category))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	if len(parts) == 0 {
		return "adapter error"
	}

	return strings.Join(parts, ": ")
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}

type Event struct {
	Delta Delta
	Err   error
}

func NewEventStream(events <-chan Event, closer io.Closer) Stream {
	return &eventStream{
		events:  events,
		closer:  closer,
		closeFn: closeIOCloser,
	}
}

func NewTextStream(parts ...string) Stream {
	cloned := append([]string(nil), parts...)
	return &textStream{parts: cloned}
}

type eventStream struct {
	events  <-chan Event
	closer  io.Closer
	closeFn func(io.Closer) error
	once    sync.Once
}

func (s *eventStream) Recv() (Delta, error) {
	if s == nil || s.events == nil {
		return Delta{}, io.EOF
	}

	event, ok := <-s.events
	if !ok {
		return Delta{}, io.EOF
	}
	if event.Err != nil {
		return Delta{}, event.Err
	}

	return event.Delta, nil
}

func (s *eventStream) Close() error {
	if s == nil {
		return nil
	}

	var err error
	s.once.Do(func() {
		err = s.closeFn(s.closer)
	})
	return err
}

type textStream struct {
	parts []string
	index int
}

func (s *textStream) Recv() (Delta, error) {
	if s == nil || s.index >= len(s.parts) {
		return Delta{}, io.EOF
	}

	part := s.parts[s.index]
	s.index++
	return Delta{Text: part}, nil
}

func (s *textStream) Close() error {
	return nil
}

func closeIOCloser(closer io.Closer) error {
	if closer == nil {
		return nil
	}

	if err := closer.Close(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}

	return nil
}
