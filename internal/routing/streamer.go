package routing

import "context"

type Streamer interface {
	Name() string
	StreamText(ctx context.Context, modelOverride string, messages []Message) (<-chan string, <-chan error)
	ResolvedModel(modelOverride string) string
	Close() error
}
