package backends

import "context"

type Request struct {
	Model   string
	Prompt  string
	Options map[string]any
}

type Response struct {
	Output   string
	Metadata map[string]any
}

type Backend interface {
	Name() string
	HealthCheck(ctx context.Context) error
	Infer(ctx context.Context, request Request) (Response, error)
}
