package vertex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/myusername/cloudinfer/internal/config"
)

type Message struct {
	Role    string
	Content string
}

type VertexAdapter struct {
	client *genai.Client
}

func NewAdapter(ctx context.Context, cfg config.VertexConfig) (*VertexAdapter, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  cfg.Project,
		Location: cfg.Location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, wrapVertexError("create client", err)
	}

	return &VertexAdapter{
		client: client,
	}, nil
}

func (a *VertexAdapter) Close() error {
	return nil
}

func (a *VertexAdapter) StreamText(ctx context.Context, modelName string, messages []Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		if a == nil || a.client == nil {
			errCh <- errors.New("vertex adapter is not configured")
			return
		}

		var (
			systemInstruction *genai.Content
			contents          = make([]*genai.Content, 0, len(messages))
		)

		for _, msg := range messages {
			role := strings.ToLower(strings.TrimSpace(msg.Role))
			if role == "system" {
				systemInstruction = &genai.Content{
					Parts: []*genai.Part{{Text: msg.Content}},
				}
				continue
			}

			vertexRole := genai.Role(genai.RoleUser)
			if role == "assistant" || role == "model" {
				vertexRole = genai.Role(genai.RoleModel)
			}

			contents = append(contents, genai.NewContentFromText(msg.Content, vertexRole))
		}

		if len(contents) == 0 {
			errCh <- errors.New("no messages provided")
			return
		}

		var genConfig *genai.GenerateContentConfig
		if systemInstruction != nil {
			genConfig = &genai.GenerateContentConfig{
				SystemInstruction: systemInstruction,
			}
		}

		for resp, err := range a.client.Models.GenerateContentStream(ctx, modelName, contents, genConfig) {
			if err != nil {
				errCh <- wrapVertexError("stream next", err)
				return
			}

			text := ""
			if resp != nil {
				text = resp.Text()
			}
			if text == "" {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case tokenCh <- text:
			}
		}
	}()

	return tokenCh, errCh
}

func wrapVertexError(operation string, err error) error {
	if err == nil {
		return nil
	}

	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf(
			"vertex %s failed (code=%d, status=%s, message=%q): %w",
			operation,
			apiErr.Code,
			apiErr.Status,
			apiErr.Message,
			err,
		)
	}

	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return fmt.Errorf("vertex %s failed: %w", operation, err)
}
