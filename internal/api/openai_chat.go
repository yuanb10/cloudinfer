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

	modelName := req.Model
	if modelName == "" {
		modelName = "mock-model"
	}

	if req.Stream {
		s.streamChatCompletion(w, r, id, created, modelName, req.Messages, start)
		return
	}

	stop := "stop"
	resp := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   modelName,
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
		s.recordChatCompletion(id, modelName, false, "internal_error", ttftMs, start, created)
		return
	}

	s.recordChatCompletion(id, modelName, false, "ok", ttftMs, start, created)
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, id string, created int64, modelName string, messages []chatMessage, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		s.recordChatCompletion(id, modelName, true, "internal_error", 0, start, created)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(http.StatusOK)

	if s.vertex == nil {
		s.streamMockChatCompletion(w, r, flusher, id, created, modelName, start)
		return
	}

	s.streamVertexChatCompletion(w, r, flusher, id, created, modelName, messages, start)
}

func (s *Server) streamMockChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, modelName string, start time.Time) {
	var ttftMs int64
	firstChunkSent := false

	for _, char := range "hello" {
		select {
		case <-r.Context().Done():
			s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
			return
		default:
		}

		chunk := chatCompletionResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   modelName,
			Choices: []chatCompletionChoice{
				{
					Index:        0,
					Delta:        &chatDelta{Content: string(char)},
					FinishReason: nil,
				},
			},
		}

		if err := writeSSEData(w, chunk); err != nil {
			s.recordChatCompletion(id, modelName, true, classifyStreamError(r), ttftMs, start, created)
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
		Model:   modelName,
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
		s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		s.recordChatCompletion(id, modelName, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		s.recordChatCompletion(id, modelName, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordChatCompletion(id, modelName, true, "ok", ttftMs, start, created)
}

func (s *Server) streamVertexChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, modelName string, messages []chatMessage, start time.Time) {
	var ttftMs int64
	firstTokenReceived := false

	// Resolve the actual Vertex model to use
	vertexModel := s.cfg.Vertex.Model
	if modelName != "" && modelName != "default" {
		vertexModel = modelName
	}

	tokenCh, errCh := s.vertex.StreamText(r.Context(), vertexModel, toVertexMessages(messages))

	for tokenCh != nil || errCh != nil {
		select {
		case <-r.Context().Done():
			s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
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
				Model:   modelName,
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
				s.logVertexStreamError(id, status, vertexModel, err)
				s.recordChatCompletion(id, modelName, true, status, ttftMs, start, created)
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
			s.logVertexStreamError(id, status, vertexModel, err)
			s.recordChatCompletion(id, modelName, true, status, ttftMs, start, created)
			return
		}
	}

	stop := "stop"
	finalChunk := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
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
		s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		status := classifyStreamError(r)
		s.logVertexStreamError(id, status, vertexModel, err)
		s.recordChatCompletion(id, modelName, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordChatCompletion(id, modelName, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		status := classifyStreamError(r)
		s.logVertexStreamError(id, status, vertexModel, err)
		s.recordChatCompletion(id, modelName, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordChatCompletion(id, modelName, true, "ok", ttftMs, start, created)
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

func (s *Server) logVertexStreamError(requestID string, status string, modelName string, err error) {
	if err == nil {
		return
	}

	if status != "provider_error" && status != "internal_error" {
		return
	}

	if s.cfg != nil {
		log.Printf(
			"vertex stream error request_id=%s project=%s location=%s model=%s err=%v",
			requestID,
			s.cfg.Vertex.Project,
			s.cfg.Vertex.Location,
			modelName,
			err,
		)
		return
	}

	log.Printf("vertex stream error request_id=%s provider=vertex model=%s err=%v", requestID, modelName, err)
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
