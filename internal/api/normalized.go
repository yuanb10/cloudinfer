package api

import (
	"strings"
	"time"

	"github.com/myusername/cloudinfer/internal/routing"
)

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

type NormalizedDelta struct {
	Text         string
	At           time.Time
	TTFT         time.Duration
	ChunkLatency time.Duration
	Sequence     int
}

func normalizeChatCompletionRequest(req chatCompletionRequest) NormalizedRequest {
	input := make([]NormalizedInputItem, 0, len(req.Messages))
	for _, message := range req.Messages {
		input = append(input, NormalizedInputItem{
			Role:    strings.TrimSpace(message.Role),
			Content: message.Content,
		})
	}

	return NormalizedRequest{
		Model:           strings.TrimSpace(req.Model),
		Input:           input,
		Stream:          req.Stream,
		MaxOutputTokens: req.MaxTokens,
		Temperature:     req.Temperature,
	}
}

func normalizeResponsesRequest(req responsesRequest) NormalizedRequest {
	input := make([]NormalizedInputItem, 0, len(req.Input))
	for _, message := range req.Input {
		input = append(input, NormalizedInputItem{
			Role:    strings.TrimSpace(message.Role),
			Content: message.Content,
		})
	}

	return NormalizedRequest{
		Model:           strings.TrimSpace(req.Model),
		Input:           input,
		Stream:          req.Stream,
		MaxOutputTokens: req.MaxOutputTokens,
		Temperature:     req.Temperature,
	}
}

func (r NormalizedRequest) RoutingMessages() []routing.Message {
	messages := make([]routing.Message, 0, len(r.Input))
	for _, message := range r.Input {
		messages = append(messages, routing.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return messages
}
