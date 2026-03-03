package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/myusername/cloudinfer/internal/adapters"
	"github.com/myusername/cloudinfer/internal/config"
)

type Adapter struct {
	client       *http.Client
	baseURL      string
	apiKey       string
	defaultModel string
	headers      map[string]string
}

func New(cfg config.OpenAIConfig) (*Adapter, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	return NewWithClient(cfg, &http.Client{Timeout: timeout})
}

func NewWithClient(cfg config.OpenAIConfig, client *http.Client) (*Adapter, error) {
	apiKey := os.Getenv(strings.TrimSpace(cfg.APIKeyEnv))
	if apiKey == "" {
		return nil, fmt.Errorf("openai-compatible adapter requires environment variable %q", cfg.APIKeyEnv)
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	return &Adapter{
		client:       client,
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

func (a *Adapter) Generate(ctx context.Context, req adapters.NormalizedRequest) (adapters.Stream, adapters.Meta, error) {
	model := a.resolveModel(req.Model)
	meta := adapters.Meta{
		Model:      model,
		StreamMode: adapters.StreamModeStream,
	}

	if a == nil || a.client == nil {
		return nil, meta, &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "openai-compatible adapter is not configured",
		}
	}

	stream, streamMeta, err := a.generateResponses(ctx, req, model)
	if err == nil {
		return stream, streamMeta, nil
	}

	var adapterErr *adapters.Error
	if errors.As(err, &adapterErr) && (adapterErr.StatusCode == http.StatusNotFound || adapterErr.StatusCode == http.StatusMethodNotAllowed) {
		return a.generateChatCompletions(ctx, req, model)
	}

	return nil, meta, err
}

func (a *Adapter) generateResponses(ctx context.Context, req adapters.NormalizedRequest, model string) (adapters.Stream, adapters.Meta, error) {
	payload := responsesRequest{
		Model:           model,
		Input:           cloneInput(req.Input),
		Stream:          true,
		MaxOutputTokens: req.MaxOutputTokens,
		Temperature:     req.Temperature,
	}

	resp, err := a.doJSONRequest(ctx, "/responses", payload)
	if err != nil {
		return nil, adapters.Meta{Model: model, StreamMode: adapters.StreamModeStream}, err
	}

	meta := adapters.Meta{Model: model, StreamMode: adapters.StreamModeStream}
	if !isSSE(resp.Header) {
		text, readErr := readResponsesJSON(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, meta, readErr
		}
		meta.StreamMode = adapters.StreamModeSingle
		return adapters.NewTextStream(text), meta, nil
	}

	return newSSEStream(resp, parseResponsesEvent), meta, nil
}

func (a *Adapter) generateChatCompletions(ctx context.Context, req adapters.NormalizedRequest, model string) (adapters.Stream, adapters.Meta, error) {
	payload := chatCompletionsRequest{
		Model:       model,
		Messages:    toChatMessages(req.Input),
		Stream:      true,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
	}

	resp, err := a.doJSONRequest(ctx, "/chat/completions", payload)
	if err != nil {
		return nil, adapters.Meta{Model: model, StreamMode: adapters.StreamModeStream}, err
	}

	meta := adapters.Meta{Model: model, StreamMode: adapters.StreamModeStream}
	if !isSSE(resp.Header) {
		text, readErr := readChatCompletionJSON(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, meta, readErr
		}
		meta.StreamMode = adapters.StreamModeSingle
		return adapters.NewTextStream(text), meta, nil
	}

	return newSSEStream(resp, parseChatCompletionEvent), meta, nil
}

func (a *Adapter) doJSONRequest(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &adapters.Error{
			Category: adapters.CategoryInvalidRequest,
			Message:  "encode request",
			Err:      err,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &adapters.Error{
			Category: adapters.CategoryInvalidRequest,
			Message:  "build request",
			Err:      err,
		}
	}

	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range a.headers {
		req.Header.Set(key, value)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, classifyTransportError(ctx, err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		if readErr != nil {
			return nil, &adapters.Error{
				Category:   adapters.CategoryUpstreamError,
				StatusCode: resp.StatusCode,
				Message:    "read error response",
				Err:        readErr,
			}
		}
		return nil, mapHTTPError(resp.StatusCode, resp.Header, body)
	}

	return resp, nil
}

func newSSEStream(resp *http.Response, parser func(string, []string) (adapters.Delta, error, bool)) adapters.Stream {
	events := make(chan adapters.Event)

	go func() {
		defer close(events)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var (
			eventName string
			eventData []string
		)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if done := emitEvent(events, eventName, eventData, parser); done {
					return
				}
				eventName = ""
				eventData = eventData[:0]
				continue
			}

			switch {
			case strings.HasPrefix(line, ":"):
				continue
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				eventData = append(eventData, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}

		if err := scanner.Err(); err != nil {
			events <- adapters.Event{Err: mapStreamReadError(err)}
		}
	}()

	return adapters.NewEventStream(events, resp.Body)
}

func emitEvent(events chan<- adapters.Event, eventName string, eventData []string, parser func(string, []string) (adapters.Delta, error, bool)) bool {
	if len(eventData) == 0 {
		return false
	}

	delta, err, done := parser(eventName, eventData)
	if err != nil {
		events <- adapters.Event{Err: err}
		return true
	}
	if delta.Text != "" {
		events <- adapters.Event{Delta: delta}
	}
	return done
}

func parseResponsesEvent(eventName string, eventData []string) (adapters.Delta, error, bool) {
	payload := strings.Join(eventData, "\n")
	if payload == "[DONE]" {
		return adapters.Delta{}, nil, true
	}

	switch eventName {
	case "", "response.output_text.delta":
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return adapters.Delta{}, &adapters.Error{
				Category: adapters.CategoryUpstreamError,
				Message:  "decode responses event",
				Err:      err,
			}, false
		}
		if event.Type == "error" {
			return adapters.Delta{}, mapEmbeddedOpenAIError(payload), false
		}
		if event.Type != "" && event.Type != "response.output_text.delta" {
			if event.Type == "response.completed" {
				return adapters.Delta{}, nil, true
			}
			return adapters.Delta{}, nil, false
		}
		return adapters.Delta{Text: event.Delta}, nil, false
	case "error":
		return adapters.Delta{}, mapEmbeddedOpenAIError(payload), false
	case "response.completed":
		return adapters.Delta{}, nil, true
	default:
		return adapters.Delta{}, nil, false
	}
}

func parseChatCompletionEvent(_ string, eventData []string) (adapters.Delta, error, bool) {
	payload := strings.Join(eventData, "\n")
	if payload == "[DONE]" {
		return adapters.Delta{}, nil, true
	}

	var chunk struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error,omitempty"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return adapters.Delta{}, &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "decode chat completion event",
			Err:      err,
		}, false
	}
	if chunk.Error != nil {
		return adapters.Delta{}, &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  chunk.Error.Message,
		}, false
	}
	if len(chunk.Choices) == 0 {
		return adapters.Delta{}, nil, false
	}

	if chunk.Choices[0].FinishReason != nil {
		return adapters.Delta{}, nil, true
	}

	return adapters.Delta{Text: chunk.Choices[0].Delta.Content}, nil, false
}

func readResponsesJSON(reader io.Reader) (string, error) {
	var body struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		return "", &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "decode responses body",
			Err:      err,
		}
	}
	if body.OutputText != "" {
		return body.OutputText, nil
	}
	for _, item := range body.Output {
		for _, part := range item.Content {
			if part.Text != "" {
				return part.Text, nil
			}
		}
	}

	return "", nil
}

func readChatCompletionJSON(reader io.Reader) (string, error) {
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		return "", &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "decode chat completion body",
			Err:      err,
		}
	}
	if len(body.Choices) == 0 {
		return "", nil
	}
	return body.Choices[0].Message.Content, nil
}

func classifyTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &adapters.Error{
			Category: adapters.CategoryTimeout,
			Message:  "request timed out",
			Err:      err,
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &adapters.Error{
			Category: adapters.CategoryTimeout,
			Message:  "request timed out",
			Err:      err,
		}
	}

	return &adapters.Error{
		Category: adapters.CategoryUpstreamError,
		Message:  "request failed",
		Err:      err,
	}
}

func mapStreamReadError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &adapters.Error{
			Category: adapters.CategoryTimeout,
			Message:  "stream timed out",
			Err:      err,
		}
	}

	return &adapters.Error{
		Category: adapters.CategoryUpstreamError,
		Message:  "read stream",
		Err:      err,
	}
}

func mapHTTPError(statusCode int, header http.Header, body []byte) error {
	message := extractErrorMessage(body)
	category := adapters.CategoryUpstreamError
	switch {
	case statusCode == http.StatusTooManyRequests:
		category = adapters.CategoryRateLimited
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		category = adapters.CategoryAuthFailed
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusGatewayTimeout:
		category = adapters.CategoryTimeout
	case statusCode >= 400 && statusCode < 500:
		category = adapters.CategoryInvalidRequest
	}

	return &adapters.Error{
		Category:   category,
		RetryAfter: parseRetryAfter(header.Get("Retry-After")),
		StatusCode: statusCode,
		Message:    message,
	}
}

func mapEmbeddedOpenAIError(payload string) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		return &adapters.Error{
			Category: adapters.CategoryUpstreamError,
			Message:  "decode upstream error",
			Err:      err,
		}
	}
	if body.Error.Message == "" {
		body.Error.Message = "upstream error"
	}
	return &adapters.Error{
		Category: adapters.CategoryUpstreamError,
		Message:  body.Error.Message,
	}
}

func extractErrorMessage(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return "upstream returned an error"
	}
	if len(text) > 256 {
		return text[:256]
	}
	return text
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}

	if deadline, err := http.ParseTime(value); err == nil {
		return time.Until(deadline)
	}

	return 0
}

func isSSE(header http.Header) bool {
	return strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream")
}

func cloneInput(input []adapters.NormalizedInputItem) []adapters.NormalizedInputItem {
	out := make([]adapters.NormalizedInputItem, 0, len(input))
	for _, item := range input {
		out = append(out, adapters.NormalizedInputItem{
			Role:    strings.TrimSpace(item.Role),
			Content: item.Content,
		})
	}
	return out
}

func toChatMessages(input []adapters.NormalizedInputItem) []chatMessage {
	out := make([]chatMessage, 0, len(input))
	for _, item := range input {
		out = append(out, chatMessage{
			Role:    strings.TrimSpace(item.Role),
			Content: item.Content,
		})
	}
	return out
}

func (a *Adapter) resolveModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "default" {
		return a.defaultModel
	}
	return model
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

type responsesRequest struct {
	Model           string                         `json:"model"`
	Input           []adapters.NormalizedInputItem `json:"input"`
	Stream          bool                           `json:"stream"`
	MaxOutputTokens int                            `json:"max_output_tokens,omitempty"`
	Temperature     float64                        `json:"temperature,omitempty"`
}

type chatCompletionsRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
