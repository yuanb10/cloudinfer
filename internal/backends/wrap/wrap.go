package wrap

import (
	"context"
	"io"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/routing"
)

type AdapterStreamer struct {
	name         string
	defaultModel string
	adapter      adapters.Adapter
}

func NewAdapterStreamer(name string, defaultModel string, adapter adapters.Adapter) *AdapterStreamer {
	return &AdapterStreamer{
		name:         name,
		defaultModel: defaultModel,
		adapter:      adapter,
	}
}

func (s *AdapterStreamer) Name() string {
	return s.name
}

func (s *AdapterStreamer) StreamText(ctx context.Context, modelOverride string, messages []routing.Message) (<-chan string, <-chan error) {
	if s == nil || s.adapter == nil {
		return closedErrorStream()
	}

	req := adapters.NormalizedRequest{
		Model:  s.ResolvedModel(modelOverride),
		Input:  toNormalizedInput(messages),
		Stream: true,
	}

	stream, _, err := s.adapter.Generate(ctx, req)
	if err != nil {
		return closedErrorStream(err)
	}

	tokenCh := make(chan string)
	errCh := make(chan error, 1)
	go func() {
		defer close(tokenCh)
		defer close(errCh)
		defer func() { _ = stream.Close() }()

		for {
			delta, err := stream.Recv()
			if err == nil {
				if delta.Text == "" {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case tokenCh <- delta.Text:
				}
				continue
			}
			if err == io.EOF {
				return
			}

			select {
			case <-ctx.Done():
			case errCh <- err:
			}
			return
		}
	}()

	return tokenCh, errCh
}

func (s *AdapterStreamer) ResolvedModel(modelOverride string) string {
	if modelOverride == "" || modelOverride == "default" {
		return s.defaultModel
	}

	return modelOverride
}

func (s *AdapterStreamer) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}

	closer, ok := s.adapter.(interface{ Close() error })
	if !ok {
		return nil
	}

	return closer.Close()
}

func toNormalizedInput(messages []routing.Message) []adapters.NormalizedInputItem {
	out := make([]adapters.NormalizedInputItem, 0, len(messages))
	for _, message := range messages {
		out = append(out, adapters.NormalizedInputItem{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return out
}

func closedErrorStream(errs ...error) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error, 1)
	if len(errs) > 0 && errs[0] != nil {
		errCh <- errs[0]
	}
	close(tokenCh)
	close(errCh)
	return tokenCh, errCh
}
