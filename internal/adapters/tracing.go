package adapters

import (
	"io"
	"sync"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func WrapStreamWithSpan(stream Stream, span trace.Span) Stream {
	if stream == nil || span == nil {
		return stream
	}

	return &tracedStream{
		stream: stream,
		span:   span,
	}
}

type tracedStream struct {
	stream         Stream
	span           trace.Span
	firstTokenOnce sync.Once
	endOnce        sync.Once
}

func (s *tracedStream) Recv() (Delta, error) {
	delta, err := s.stream.Recv()
	if err == nil && delta.Text != "" {
		s.firstTokenOnce.Do(func() {
			s.span.AddEvent("first_token")
		})
		return delta, nil
	}

	if err == io.EOF {
		s.end(nil)
		return delta, err
	}
	if err != nil {
		s.end(err)
		return delta, err
	}

	return delta, nil
}

func (s *tracedStream) Close() error {
	err := s.stream.Close()
	s.end(err)
	return err
}

func (s *tracedStream) end(err error) {
	s.endOnce.Do(func() {
		if err != nil && err != io.EOF {
			s.span.RecordError(err)
			s.span.SetStatus(codes.Error, err.Error())
		}
		s.span.End()
	})
}
