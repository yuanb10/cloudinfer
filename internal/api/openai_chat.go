package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	openaibackend "github.com/myusername/cloudinfer/internal/backends/openai"
	"github.com/myusername/cloudinfer/internal/backends/vertex"
	"github.com/myusername/cloudinfer/internal/telemetry"
)

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Index        int          `json:"index"`
	Message      *chatMessage `json:"message,omitempty"`
	Delta        *chatDelta   `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

type chatDelta struct {
	Content string `json:"content,omitempty"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := newChatCompletionID()
	created := start.Unix()
	w.Header().Set("X-Request-Id", id)

	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		s.recordChatCompletion(id, "unknown", false, "bad_request", 0, start, created)
		return
	}

	requestModel := req.Model
	if requestModel == "" {
		requestModel = "mock-model"
	}
	responseModel := s.resolvedResponseModel(requestModel)

	if req.Stream {
		s.streamChatCompletion(w, r, id, created, responseModel, req.Messages, start)
		return
	}

	stop := "stop"
	resp := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   responseModel,
		Choices: []chatCompletionChoice{
			{
				Index: 0,
				Message: &chatMessage{
					Role:    "assistant",
					Content: "hello",
				},
				FinishReason: &stop,
			},
		},
	}
	ttftMs := time.Since(start).Milliseconds()
	if err := writeJSON(w, http.StatusOK, resp); err != nil {
		s.recordChatCompletion(id, responseModel, false, "internal_error", ttftMs, start, created)
		return
	}

	s.recordChatCompletion(id, responseModel, false, "ok", ttftMs, start, created)
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, id string, created int64, responseModel string, messages []chatMessage, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		s.recordChatCompletion(id, responseModel, true, "internal_error", 0, start, created)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(http.StatusOK)

	if s.cfg != nil && s.cfg.Backend == "openai" && s.openai != nil {
		s.streamOpenAIChatCompletion(w, r, flusher, id, created, reqModelForStream(responseModel, s.openai), messages, start)
		return
	}

	if s.cfg != nil && s.cfg.Backend == "vertex" && s.vertex != nil {
		s.streamVertexChatCompletion(w, r, flusher, id, created, responseModel, messages, start)
		return
	}

	s.streamMockChatCompletion(w, r, flusher, id, created, responseModel, start)
}

func (s *Server) streamMockChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, responseModel string, start time.Time) {
	var ttftMs int64
	firstChunkSent := false

	for _, char := range "hello" {
		select {
		case <-r.Context().Done():
			s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
			return
		default:
		}

		chunk := chatCompletionResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   responseModel,
			Choices: []chatCompletionChoice{
				{
					Index:        0,
					Delta:        &chatDelta{Content: string(char)},
					FinishReason: nil,
				},
			},
		}

		if err := writeSSEData(w, chunk); err != nil {
			s.recordChatCompletion(id, responseModel, true, classifyStreamError(r), ttftMs, start, created)
			return
		}
		if !firstChunkSent {
			ttftMs = time.Since(start).Milliseconds()
			firstChunkSent = true
		}
		flusher.Flush()
	}

	stop := "stop"
	finalChunk := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   responseModel,
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Delta:        &chatDelta{},
				FinishReason: &stop,
			},
		},
	}

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		s.recordChatCompletion(id, responseModel, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		s.recordChatCompletion(id, responseModel, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordChatCompletion(id, responseModel, true, "ok", ttftMs, start, created)
}

func (s *Server) streamVertexChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, responseModel string, messages []chatMessage, start time.Time) {
	var ttftMs int64
	firstTokenReceived := false

	vertexModel := responseModel

	tokenCh, errCh := s.vertex.StreamText(r.Context(), vertexModel, toVertexMessages(messages))

	for tokenCh != nil || errCh != nil {
		select {
		case <-r.Context().Done():
			s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
			return
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}

			if !firstTokenReceived {
				ttftMs = time.Since(start).Milliseconds()
				firstTokenReceived = true
			}

			chunk := chatCompletionResponse{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   responseModel,
				Choices: []chatCompletionChoice{
					{
						Index:        0,
						Delta:        &chatDelta{Content: token},
						FinishReason: nil,
					},
				},
			}

			if err := writeSSEData(w, chunk); err != nil {
				status := classifyStreamError(r)
				s.logVertexStreamError(id, status, err)
				s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
				return
			}
			flusher.Flush()
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}

			status := classifyProviderError(r)
			s.logVertexStreamError(id, status, err)
			s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
			return
		}
	}

	stop := "stop"
	finalChunk := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   responseModel,
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Delta:        &chatDelta{},
				FinishReason: &stop,
			},
		},
	}

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		status := classifyStreamError(r)
		s.logVertexStreamError(id, status, err)
		s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		status := classifyStreamError(r)
		s.logVertexStreamError(id, status, err)
		s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordChatCompletion(id, responseModel, true, "ok", ttftMs, start, created)
}

func (s *Server) streamOpenAIChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, responseModel string, messages []chatMessage, start time.Time) {
	var ttftMs int64
	firstTokenReceived := false

	tokenCh, errCh := s.openai.StreamText(r.Context(), responseModel, toOpenAIMessages(messages))

	for tokenCh != nil || errCh != nil {
		select {
		case <-r.Context().Done():
			s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
			return
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}

			if !firstTokenReceived {
				ttftMs = time.Since(start).Milliseconds()
				firstTokenReceived = true
			}

			chunk := chatCompletionResponse{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   responseModel,
				Choices: []chatCompletionChoice{
					{
						Index:        0,
						Delta:        &chatDelta{Content: token},
						FinishReason: nil,
					},
				},
			}

			if err := writeSSEData(w, chunk); err != nil {
				status := classifyStreamError(r)
				s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
				return
			}
			flusher.Flush()
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}

			status := classifyProviderError(r)
			s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
			return
		}
	}

	stop := "stop"
	finalChunk := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   responseModel,
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Delta:        &chatDelta{},
				FinishReason: &stop,
			},
		},
	}

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		status := classifyStreamError(r)
		s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		status := classifyStreamError(r)
		s.recordChatCompletion(id, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordChatCompletion(id, responseModel, true, "ok", ttftMs, start, created)
}

func writeSSEData(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}

	return nil
}

func classifyStreamError(r *http.Request) string {
	if r.Context().Err() != nil {
		return "client_cancel"
	}

	return "internal_error"
}

func classifyProviderError(r *http.Request) string {
	if errors.Is(r.Context().Err(), context.Canceled) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		return "client_cancel"
	}

	return "provider_error"
}

func (s *Server) recordChatCompletion(requestID string, modelName string, stream bool, status string, ttftMs int64, start time.Time, created int64) {
	totalLatencyMs := time.Since(start).Milliseconds()

	if s.metrics != nil {
		s.metrics.Observe(status, stream, ttftMs, totalLatencyMs)
	}

	if s.logger != nil {
		s.logger.Log(telemetry.TelemetryEvent{
			RequestID:      requestID,
			Model:          modelName,
			Stream:         stream,
			Status:         status,
			TTFTms:         ttftMs,
			TotalLatencyms: totalLatencyMs,
			CreatedUnix:    created,
		})
	}
}

func (s *Server) logVertexStreamError(requestID string, status string, err error) {
	if err == nil {
		return
	}

	if status != "provider_error" {
		return
	}

	if s.cfg != nil {
		log.Printf(
			"vertex stream error request_id=%s project=%s location=%s model=%s err=%v",
			requestID,
			s.cfg.Vertex.Project,
			s.cfg.Vertex.Location,
			s.cfg.Vertex.Model,
			err,
		)
		return
	}

	log.Printf("vertex stream error request_id=%s project= location= model= err=%v", requestID, err)
}

func newChatCompletionID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}

	return "chatcmpl-" + hex.EncodeToString(raw[:])
}

func toVertexMessages(messages []chatMessage) []vertex.Message {
	vertexMessages := make([]vertex.Message, 0, len(messages))
	for _, message := range messages {
		vertexMessages = append(vertexMessages, vertex.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return vertexMessages
}

func toOpenAIMessages(messages []chatMessage) []openaibackend.Message {
	openAIMessages := make([]openaibackend.Message, 0, len(messages))
	for _, message := range messages {
		openAIMessages = append(openAIMessages, openaibackend.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	return openAIMessages
}

func (s *Server) resolvedResponseModel(requestModel string) string {
	if s != nil && s.cfg != nil && s.cfg.Backend == "openai" && s.openai != nil {
		return s.openai.ResolvedModel(requestModel)
	}

	if s != nil && s.vertex != nil && s.cfg != nil && s.cfg.Backend == "vertex" && s.cfg.Vertex.Model != "" {
		return s.cfg.Vertex.Model
	}

	return requestModel
}

func reqModelForStream(responseModel string, adapter *openaibackend.Adapter) string {
	if adapter == nil {
		return responseModel
	}

	return responseModel
}
