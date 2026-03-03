package vertex

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"google.golang.org/genai"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/config"
)

type Client interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
	Close() error
}

type Adapter struct {
	client       Client
	defaultModel string
}

func New(ctx context.Context, cfg config.VertexConfig) (*Adapter, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  cfg.Project,
		Location: cfg.Location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, mapVertexInitError("create client", err)
	}

	return NewWithClient(cfg.Model, &genaiClient{client: client}), nil
}

func NewWithClient(defaultModel string, client Client) *Adapter {
	return &Adapter{
		client:       client,
		defaultModel: strings.TrimSpace(defaultModel),
	}
}

func (a *Adapter) Name() string {
	return "vertex-adc"
}

func (a *Adapter) Close() error {
	if a == nil || a.client == nil {
		return nil
	}

	return a.client.Close()
}

func (a *Adapter) Generate(ctx context.Context, req adapters.NormalizedRequest) (adapters.Stream, adapters.Meta, error) {
	model := a.resolveModel(req.Model)
	meta := adapters.Meta{
		Model:      model,
		StreamMode: adapters.StreamModeStream,
	}

	if a == nil || a.client == nil {
		return nil, meta, &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "vertex adapter is not configured",
		}
	}

	contents, genConfig, err := buildRequest(req)
	if err != nil {
		return nil, meta, err
	}

	if !req.Stream {
		resp, err := a.client.GenerateContent(ctx, model, contents, genConfig)
		if err != nil {
			return nil, adapters.Meta{Model: model, StreamMode: adapters.StreamModeSingle}, mapVertexRuntimeError("generate content", err)
		}
		return adapters.NewTextStream(responseText(resp)), adapters.Meta{Model: model, StreamMode: adapters.StreamModeSingle}, nil
	}

	events := make(chan adapters.Event)
	go func() {
		defer close(events)

		sentSoFar := ""
		for resp, err := range a.client.GenerateContentStream(ctx, model, contents, genConfig) {
			if err != nil {
				events <- adapters.Event{Err: mapVertexRuntimeError("stream next", err)}
				return
			}

			text := responseText(resp)
			if text == "" {
				continue
			}

			suffix := text
			if strings.HasPrefix(text, sentSoFar) {
				suffix = text[len(sentSoFar):]
			}
			sentSoFar = text
			if suffix == "" {
				continue
			}

			events <- adapters.Event{Delta: adapters.Delta{Text: suffix}}
		}
	}()

	return adapters.NewEventStream(events, nil), meta, nil
}

func buildRequest(req adapters.NormalizedRequest) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	var (
		systemInstruction *genai.Content
		contents          = make([]*genai.Content, 0, len(req.Input))
	)

	for _, item := range req.Input {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "system" {
			systemInstruction = &genai.Content{
				Parts: []*genai.Part{{Text: item.Content}},
			}
			continue
		}

		vertexRole := genai.Role(genai.RoleUser)
		if role == "assistant" || role == "model" {
			vertexRole = genai.Role(genai.RoleModel)
		}

		contents = append(contents, genai.NewContentFromText(item.Content, vertexRole))
	}

	if len(contents) == 0 {
		return nil, nil, &adapters.Error{
			Category: adapters.CategoryInvalidRequest,
			Message:  "no messages provided",
		}
	}

	var genConfig *genai.GenerateContentConfig
	if systemInstruction != nil || req.MaxOutputTokens > 0 || req.Temperature > 0 {
		genConfig = &genai.GenerateContentConfig{}
		if systemInstruction != nil {
			genConfig.SystemInstruction = systemInstruction
		}
		if req.MaxOutputTokens > 0 {
			genConfig.MaxOutputTokens = int32(req.MaxOutputTokens)
		}
		if req.Temperature > 0 {
			temperature := float32(req.Temperature)
			genConfig.Temperature = &temperature
		}
	}

	return contents, genConfig, nil
}

func responseText(resp *genai.GenerateContentResponse) string {
	if resp == nil {
		return ""
	}

	return resp.Text()
}

func mapVertexInitError(operation string, err error) error {
	if err == nil {
		return nil
	}

	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return &adapters.Error{
			Category:   vertexCategory(apiErr.Code),
			StatusCode: apiErr.Code,
			Message:    fmt.Sprintf("%s: %s", operation, apiErr.Message),
			Err:        err,
		}
	}

	return &adapters.Error{
		Category: adapters.CategoryUpstreamError,
		Message:  fmt.Sprintf("%s failed", operation),
		Err:      err,
	}
}

func mapVertexRuntimeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &adapters.Error{
			Category: adapters.CategoryTimeout,
			Message:  fmt.Sprintf("%s timed out", operation),
			Err:      err,
		}
	}

	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return &adapters.Error{
			Category:   vertexCategory(apiErr.Code),
			StatusCode: apiErr.Code,
			Message:    apiErr.Message,
			Err:        err,
		}
	}

	return &adapters.Error{
		Category: adapters.CategoryUpstreamError,
		Message:  fmt.Sprintf("%s failed", operation),
		Err:      err,
	}
}

func vertexCategory(statusCode int) adapters.ErrorCategory {
	switch {
	case statusCode == 429:
		return adapters.CategoryRateLimited
	case statusCode == 401 || statusCode == 403:
		return adapters.CategoryAuthFailed
	case statusCode == 408 || statusCode == 504:
		return adapters.CategoryTimeout
	case statusCode >= 400 && statusCode < 500:
		return adapters.CategoryInvalidRequest
	default:
		return adapters.CategoryUpstreamError
	}
}

func (a *Adapter) resolveModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "default" {
		if a.defaultModel != "" {
			return a.defaultModel
		}
	}

	return model
}

type genaiClient struct {
	client *genai.Client
}

func (c *genaiClient) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return c.client.Models.GenerateContent(ctx, model, contents, config)
}

func (c *genaiClient) GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return c.client.Models.GenerateContentStream(ctx, model, contents, config)
}

func (c *genaiClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}

	if closer, ok := any(c.client).(interface{ Close() error }); ok {
		return closer.Close()
	}

	return nil
}
