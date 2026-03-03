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
	"strings"
	"time"

	"github.com/myusername/cloudinfer/internal/routing"
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

const chatCompletionsEndpoint = "/v1/chat/completions"

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := newChatCompletionID()
	created := start.Unix()
	w.Header().Set("X-Request-Id", id)

	if s.lc != nil && s.lc.Draining() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		s.recordRequest(chatCompletionsEndpoint, id, "unknown", "unknown", false, "draining", -1, start, created)
		return
	}

	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		s.recordRequest(chatCompletionsEndpoint, id, "unknown", "unknown", false, "bad_request", -1, start, created)
		return
	}

	normalized := normalizeChatCompletionRequest(req)
	normalized.Model = defaultRequestModel(normalized.Model)

	if normalized.Stream {
		s.streamChatCompletion(w, r, id, created, normalized, start)
		return
	}

	stop := "stop"
	resp := chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   normalized.Model,
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
		s.recordRequest(chatCompletionsEndpoint, id, "mock", normalized.Model, false, "internal_error", ttftMs, start, created)
		return
	}

	s.recordRequest(chatCompletionsEndpoint, id, "mock", normalized.Model, false, "ok", ttftMs, start, created)
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, id string, created int64, req NormalizedRequest, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		s.recordRequest(chatCompletionsEndpoint, id, "unknown", req.Model, true, "internal_error", -1, start, created)
		return
	}

	decision, backendName, responseModel := s.selectExecutionTarget(w, id, req.Model, true)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(http.StatusOK)

	var release func()
	if s.lc != nil {
		if !s.lc.TryAcquireStream() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "draining", -1, start, created)
			return
		}
		release = func() { s.lc.ReleaseStream() }
		defer release()
	}

	if decision.Chosen.Client != nil {
		s.streamRoutedChatCompletion(w, r, flusher, id, created, backendName, responseModel, req.RoutingMessages(), decision, start)
		return
	}

	s.streamMockChatCompletion(w, r, flusher, id, created, backendName, responseModel, start)
}

func (s *Server) streamMockChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, backendName string, responseModel string, start time.Time) {
	var ttftMs int64 = -1
	firstChunkSent := false

	for _, char := range "hello" {
		select {
		case <-r.Context().Done():
			s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
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
			s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, classifyStreamError(r), ttftMs, start, created)
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
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, classifyStreamError(r), ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "ok", ttftMs, start, created)
}

func (s *Server) streamRoutedChatCompletion(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, backendName string, responseModel string, messages []routing.Message, decision routing.Decision, start time.Time) {
	var ttftMs int64 = -1
	firstTokenReceived := false

	tokenCh, errCh := decision.Chosen.Client.StreamText(r.Context(), responseModel, messages)

	for tokenCh != nil || errCh != nil {
		select {
		case <-r.Context().Done():
			s.observeRoute(decision, "client_cancel", ttftMs)
			s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
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
				s.observeRoute(decision, status, ttftMs)
				s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
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
			if status == "provider_error" {
				log.Printf("backend stream error request_id=%s backend=%s backend_type=%s model=%s err=%v", id, decision.Chosen.Name, decision.Chosen.Type, responseModel, err)
			}
			s.observeRoute(decision, status, ttftMs)
			s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
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
		s.observeRoute(decision, "client_cancel", ttftMs)
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if err := writeSSEData(w, finalChunk); err != nil {
		status := classifyStreamError(r)
		s.observeRoute(decision, status, ttftMs)
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	select {
	case <-r.Context().Done():
		s.observeRoute(decision, "client_cancel", ttftMs)
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
		return
	default:
	}

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		status := classifyStreamError(r)
		s.observeRoute(decision, status, ttftMs)
		s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.observeRoute(decision, "ok", ttftMs)
	s.recordRequest(chatCompletionsEndpoint, id, backendName, responseModel, true, "ok", ttftMs, start, created)
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

func (s *Server) recordRequest(endpoint string, requestID string, backendName string, modelName string, stream bool, status string, ttftMs int64, start time.Time, created int64) {
	totalLatency := time.Since(start)

	if s.metrics != nil {
		s.metrics.ObserveChatCompletion(
			endpoint,
			backendName,
			modelName,
			status,
			time.Duration(ttftMs)*time.Millisecond,
			totalLatency,
			stream,
		)
	}

	if s.logger != nil {
		s.logger.Log(telemetry.TelemetryEvent{
			Endpoint:       endpoint,
			RequestID:      requestID,
			Model:          modelName,
			Stream:         stream,
			Status:         status,
			TTFTms:         ttftMs,
			TotalLatencyms: totalLatency.Milliseconds(),
			CreatedUnix:    created,
		})
	}
}

func (s *Server) logRouteDecision(requestID string, decision routing.Decision) {
	if decision.Chosen.Name == "" {
		return
	}

	log.Printf(
		"route decision request_id=%s backend=%s backend_type=%s reason=%s candidates=%s",
		requestID,
		decision.Chosen.Name,
		decision.Chosen.Type,
		decision.Reason,
		formatCandidates(decision.Candidates),
	)
}

func newChatCompletionID() string {
	return newGeneratedID("chatcmpl-", "chatcmpl")
}

func newResponseID() string {
	return newGeneratedID("resp_", "resp")
}

func newGeneratedID(prefix string, fallbackPrefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", fallbackPrefix, time.Now().UnixNano())
	}

	return prefix + hex.EncodeToString(raw[:])
}

func (s *Server) observeRoute(decision routing.Decision, status string, ttftMs int64) {
	if s == nil || s.router == nil || !s.router.Enabled() || decision.Chosen.Name == "" {
		return
	}

	s.router.Observe(decision.Chosen.Name, status, ttftMs, status == "provider_error")
}

func formatCandidates(candidates []routing.CandidateDecision) string {
	if len(candidates) == 0 {
		return ""
	}

	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		score := "inf"
		if candidate.Score < 1e308 {
			score = fmt.Sprintf("%.0f", candidate.Score)
		}

		parts = append(parts, fmt.Sprintf(
			"%s:%s:eligible=%t:score=%s:samples=%d",
			candidate.Name,
			candidate.Type,
			candidate.Eligible,
			score,
			candidate.Stats.Samples,
		))
	}

	return strings.Join(parts, ",")
}
