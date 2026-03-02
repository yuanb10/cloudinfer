package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
)

type Message struct {
	Role    string
	Content string
}

type Adapter struct {
	client       *http.Client
	baseURL      string
	apiKey       string
	defaultModel string
	headers      map[string]string
}

type streamRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages []streamMessage `json:"messages"`
}

type streamMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func New(cfg config.OpenAIConfig) (*Adapter, error) {
	apiKey := os.Getenv(strings.TrimSpace(cfg.APIKeyEnv))
	if apiKey == "" {
		return nil, fmt.Errorf("openai-compatible adapter requires environment variable %q", cfg.APIKeyEnv)
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	return &Adapter{
		client: &http.Client{
			Timeout: timeout,
		},
		baseURL:      normalizeBaseURL(cfg.BaseURL),
		apiKey:       apiKey,
		defaultModel: defaultModel(cfg.Model),
		headers:      cloneHeaders(cfg.ExtraHeaders),
	}, nil
}

func (a *Adapter) Name() string {
	return "openai-compatible"
}

func (a *Adapter) Close() error {
	return nil
}

func (a *Adapter) ResolvedModel(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || modelName == "default" {
		return a.defaultModel
	}

	return modelName
}

func (a *Adapter) StreamText(ctx context.Context, modelName string, messages []Message) (<-chan string, <-chan error) {
	tokenCh := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		if a == nil || a.client == nil {
			sendTerminalError(errCh, errors.New("openai-compatible adapter is not configured"))
			return
		}

		payload := streamRequest{
			Model:    a.ResolvedModel(modelName),
			Stream:   true,
			Messages: toStreamMessages(messages),
		}

		body, err := json.Marshal(payload)
		if err != nil {
			sendTerminalError(errCh, fmt.Errorf("provider_error: encode request: %w", err))
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			sendTerminalError(errCh, fmt.Errorf("provider_error: build request: %w", err))
			return
		}

		req.Header.Set("Authorization", "Bearer "+a.apiKey)
		req.Header.Set("Content-Type", "application/json")
		for key, value := range a.headers {
			req.Header.Set(key, value)
		}

		resp, err := a.client.Do(req)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				sendTerminalError(errCh, ctx.Err())
				return
			}
			sendTerminalError(errCh, fmt.Errorf("provider_error: request failed: %w", err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if readErr != nil {
				sendTerminalError(errCh, fmt.Errorf("provider_error: status=%d body_read_error=%v", resp.StatusCode, readErr))
				return
			}
			sendTerminalError(errCh, fmt.Errorf("provider_error: status=%d body=%q", resp.StatusCode, string(body)))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var (
			eventData []string
			sawDone   bool
		)

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				done, err := emitSSEEvent(ctx, tokenCh, eventData)
				if err != nil {
					sendTerminalError(errCh, err)
					return
				}
				if done {
					sawDone = true
					return
				}
				eventData = eventData[:0]
				continue
			}

			if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			eventData = append(eventData, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}

		if err := scanner.Err(); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				sendTerminalError(errCh, ctx.Err())
				return
			}
			sendTerminalError(errCh, fmt.Errorf("provider_error: read stream: %w", err))
			return
		}

		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			sendTerminalError(errCh, ctx.Err())
			return
		}

		if !sawDone {
			sendTerminalError(errCh, errors.New("provider_error: unexpected EOF"))
		}
	}()

	return tokenCh, errCh
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}

	return baseURL + "/v1"
}

func defaultModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "gpt-4o-mini"
	}

	return model
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}

	return cloned
}

func toStreamMessages(messages []Message) []streamMessage {
	out := make([]streamMessage, 0, len(messages))
	for _, message := range messages {
		out = append(out, streamMessage{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return out
}

func sendTerminalError(errCh chan<- error, err error) {
	if err == nil {
		return
	}

	select {
	case errCh <- err:
	default:
	}
}

func emitSSEEvent(ctx context.Context, tokenCh chan<- string, eventData []string) (bool, error) {
	if len(eventData) == 0 {
		return false, nil
	}

	payload := strings.Join(eventData, "\n")
	if payload == "[DONE]" {
		return true, nil
	}

	var chunk streamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false, fmt.Errorf("provider_error: decode stream chunk: %w", err)
	}

	if len(chunk.Choices) == 0 {
		return false, nil
	}

	content := chunk.Choices[0].Delta.Content
	if content == "" {
		return false, nil
	}

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case tokenCh <- content:
		return false, nil
	}
}
