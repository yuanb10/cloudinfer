package wrap

import (
	"context"

	openaibackend "github.com/myusername/cloudinfer/internal/backends/openai"
	"github.com/myusername/cloudinfer/internal/backends/vertex"
	"github.com/myusername/cloudinfer/internal/routing"
)

type OpenAIStreamer struct {
	name    string
	adapter *openaibackend.Adapter
}

func NewOpenAIStreamer(name string, adapter *openaibackend.Adapter) *OpenAIStreamer {
	return &OpenAIStreamer{name: name, adapter: adapter}
}

func (s *OpenAIStreamer) Name() string {
	return s.name
}

func (s *OpenAIStreamer) StreamText(ctx context.Context, modelOverride string, messages []routing.Message) (<-chan string, <-chan error) {
	if s == nil || s.adapter == nil {
		return closedErrorStream()
	}

	return s.adapter.StreamText(ctx, modelOverride, toOpenAIMessages(messages))
}

func (s *OpenAIStreamer) ResolvedModel(modelOverride string) string {
	if s == nil || s.adapter == nil {
		return modelOverride
	}

	return s.adapter.ResolvedModel(modelOverride)
}

func (s *OpenAIStreamer) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}

	return s.adapter.Close()
}

type VertexStreamer struct {
	name         string
	defaultModel string
	adapter      *vertex.VertexAdapter
}

func NewVertexStreamer(name string, defaultModel string, adapter *vertex.VertexAdapter) *VertexStreamer {
	return &VertexStreamer{
		name:         name,
		defaultModel: defaultModel,
		adapter:      adapter,
	}
}

func (s *VertexStreamer) Name() string {
	return s.name
}

func (s *VertexStreamer) StreamText(ctx context.Context, modelOverride string, messages []routing.Message) (<-chan string, <-chan error) {
	if s == nil || s.adapter == nil {
		return closedErrorStream()
	}

	return s.adapter.StreamText(ctx, s.ResolvedModel(modelOverride), toVertexMessages(messages))
}

func (s *VertexStreamer) ResolvedModel(modelOverride string) string {
	if modelOverride == "" || modelOverride == "default" {
		return s.defaultModel
	}

	return modelOverride
}

func (s *VertexStreamer) Close() error {
	if s == nil || s.adapter == nil {
		return nil
	}

	return s.adapter.Close()
}

func toOpenAIMessages(messages []routing.Message) []openaibackend.Message {
	out := make([]openaibackend.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, openaibackend.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return out
}

func toVertexMessages(messages []routing.Message) []vertex.Message {
	out := make([]vertex.Message, 0, len(messages))
	for _, message := range messages {
		out = append(out, vertex.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return out
}

func closedErrorStream() (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error)
	close(tokenCh)
	close(errCh)
	return tokenCh, errCh
}
