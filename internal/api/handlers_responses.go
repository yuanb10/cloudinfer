package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	openaitypes "github.com/myusername/cloudinfer/internal/openai"
	"github.com/myusername/cloudinfer/internal/routing"
)

const responsesEndpoint = "/v1/responses"

type responsesRequest struct {
	Model           string
	Input           []responsesInputItem
	Stream          bool
	MaxOutputTokens int
	Temperature     float64
}

type responsesInputItem struct {
	Role    string
	Content string
}

type responsesInputTextPart struct {
	Type string
	Text string
}

type responsesErrorEnvelope struct {
	Error openaitypes.ErrorDetail `json:"error"`
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := newResponseID()
	created := start.Unix()
	w.Header().Set("X-Request-Id", id)

	if s.lc != nil && s.lc.Draining() {
		s.writeResponsesError(w, http.StatusServiceUnavailable, "draining", "server_error")
		s.recordRequest(responsesEndpoint, id, "unknown", "unknown", false, "draining", -1, start, created)
		return
	}

	req, err := parseResponsesRequest(r.Body)
	if err != nil {
		s.writeResponsesError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		s.recordRequest(responsesEndpoint, id, "unknown", "unknown", false, "bad_request", -1, start, created)
		return
	}

	normalized := normalizeResponsesRequest(req)
	normalized.Model = defaultRequestModel(normalized.Model)

	if normalized.Stream {
		s.streamResponses(w, r, id, created, normalized, start)
		return
	}

	s.respondResponses(w, r, id, created, normalized, start)
}

func (s *Server) respondResponses(w http.ResponseWriter, r *http.Request, id string, created int64, req NormalizedRequest, start time.Time) {
	backendName := "mock"
	responseModel := req.Model
	var decision routing.Decision

	prepared, status, err := s.prepareBackendStream(r.Context(), req.Model, req.RoutingMessages())
	if err != nil {
		if status == "client_cancel" {
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, status, -1, start, created)
			return
		}

		s.writeResponsesError(w, statusCodeForStatus(status), responseErrorMessage(status), errorTypeForStatus(status))
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, status, -1, start, created)
		return
	}
	if prepared != nil {
		defer prepared.Close()
		decision = prepared.decision
		backendName = prepared.backendName
		responseModel = prepared.responseModel
	}

	outputText, ttftMs, status, err := s.collectResponsesOutput(r.Context(), req, decision, responseModel, prepared, start)
	if err != nil {
		if status == "client_cancel" {
			s.observeRoute(decision, status, ttftMs)
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, status, ttftMs, start, created)
			return
		}

		s.observeRouteWithError(decision, status, ttftMs, err)
		s.writeResponsesError(w, statusCodeForStatus(status), responseErrorMessage(status), errorTypeForStatus(status))
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, status, ttftMs, start, created)
		return
	}

	resp := openaitypes.NewResponse(id, responseModel, "completed", created, outputText)
	if err := writeJSON(w, http.StatusOK, resp); err != nil {
		s.observeRoute(decision, "internal_error", ttftMs)
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, "internal_error", ttftMs, start, created)
		return
	}

	s.observeRoute(decision, "ok", ttftMs)
	s.recordRequest(responsesEndpoint, id, backendName, responseModel, false, "ok", ttftMs, start, created)
}

func (s *Server) streamResponses(w http.ResponseWriter, r *http.Request, id string, created int64, req NormalizedRequest, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeResponsesError(w, http.StatusInternalServerError, "streaming unsupported", "server_error")
		s.recordRequest(responsesEndpoint, id, "unknown", req.Model, true, "internal_error", -1, start, created)
		return
	}

	var release func()
	if s.lc != nil {
		if !s.lc.TryAcquireStream() {
			s.writeResponsesError(w, http.StatusServiceUnavailable, "draining", "server_error")
			s.recordRequest(responsesEndpoint, id, "unknown", req.Model, true, "draining", -1, start, created)
			return
		}
		release = func() { s.lc.ReleaseStream() }
		defer release()
	}

	backendName := "mock"
	responseModel := req.Model
	var decision routing.Decision

	prepared, status, err := s.prepareBackendStream(r.Context(), req.Model, req.RoutingMessages())
	if err != nil {
		if status == "client_cancel" {
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, -1, start, created)
			return
		}

		s.writeResponsesError(w, statusCodeForStatus(status), responseErrorMessage(status), errorTypeForStatus(status))
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, -1, start, created)
		return
	}
	if prepared != nil {
		decision = prepared.decision
		backendName = prepared.backendName
		responseModel = prepared.responseModel
		s.applyRouteHeaders(w, id, decision, responseModel)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(http.StatusOK)

	createdEvent := openaitypes.NewResponseCreatedEvent(openaitypes.NewResponse(id, responseModel, "in_progress", created, ""))
	if err := writeSSEEvent(w, createdEvent.Type, createdEvent); err != nil {
		status := classifyStreamError(r)
		s.observeRoute(decision, status, -1)
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, -1, start, created)
		return
	}
	flusher.Flush()

	if prepared != nil {
		s.streamRoutedResponse(w, r, flusher, id, created, backendName, responseModel, prepared, start)
		return
	}

	s.streamMockResponse(w, r, flusher, id, created, backendName, responseModel, start)
}

func (s *Server) streamMockResponse(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, backendName string, responseModel string, start time.Time) {
	var (
		builder       strings.Builder
		ttftMs        int64 = -1
		lastChunkTime       = start
	)

	for sequence, char := range "hello" {
		select {
		case <-r.Context().Done():
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
			return
		default:
		}

		now := time.Now()
		delta := NormalizedDelta{
			Text:         string(char),
			At:           now,
			ChunkLatency: now.Sub(lastChunkTime),
			Sequence:     sequence,
		}
		if sequence == 0 {
			delta.TTFT = now.Sub(start)
			ttftMs = delta.TTFT.Milliseconds()
		}
		lastChunkTime = now
		builder.WriteString(delta.Text)

		event := openaitypes.NewResponseOutputTextDeltaEvent(
			id,
			delta.Text,
			delta.Sequence,
			delta.TTFT.Milliseconds(),
			delta.ChunkLatency.Milliseconds(),
		)
		if err := writeSSEEvent(w, event.Type, event); err != nil {
			status := classifyStreamError(r)
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
			return
		}
		flusher.Flush()
	}

	completedEvent := openaitypes.NewResponseCompletedEvent(openaitypes.NewResponse(id, responseModel, "completed", created, builder.String()))
	if err := writeSSEEvent(w, completedEvent.Type, completedEvent); err != nil {
		status := classifyStreamError(r)
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, "ok", ttftMs, start, created)
}

func (s *Server) streamRoutedResponse(w http.ResponseWriter, r *http.Request, flusher http.Flusher, id string, created int64, backendName string, responseModel string, prepared *preparedBackendStream, start time.Time) {
	defer prepared.Close()

	var (
		builder            strings.Builder
		ttftMs             int64 = -1
		firstTokenReceived       = false
		lastChunkTime            = start
		sequence                 = 0
	)

	tokenCh, errCh := prepared.tokenCh, prepared.errCh

	if prepared.firstToken != "" {
		delta := NormalizedDelta{
			Text:         prepared.firstToken,
			At:           prepared.firstTokenAt,
			ChunkLatency: prepared.firstTokenAt.Sub(start),
			Sequence:     sequence,
			TTFT:         prepared.firstTokenAt.Sub(start),
		}
		ttftMs = delta.TTFT.Milliseconds()
		firstTokenReceived = true
		lastChunkTime = prepared.firstTokenAt
		sequence++
		builder.WriteString(prepared.firstToken)

		event := openaitypes.NewResponseOutputTextDeltaEvent(
			id,
			prepared.firstToken,
			delta.Sequence,
			delta.TTFT.Milliseconds(),
			delta.ChunkLatency.Milliseconds(),
		)
		if err := writeSSEEvent(w, event.Type, event); err != nil {
			status := classifyStreamError(r)
			s.observeRoute(prepared.decision, status, ttftMs)
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
			return
		}
		flusher.Flush()
	}

	for tokenCh != nil || errCh != nil {
		select {
		case <-r.Context().Done():
			s.observeRoute(prepared.decision, "client_cancel", ttftMs)
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, "client_cancel", ttftMs, start, created)
			return
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}

			now := time.Now()
			delta := NormalizedDelta{
				Text:         token,
				At:           now,
				ChunkLatency: now.Sub(lastChunkTime),
				Sequence:     sequence,
			}
			if !firstTokenReceived {
				delta.TTFT = now.Sub(start)
				ttftMs = delta.TTFT.Milliseconds()
				firstTokenReceived = true
			}
			lastChunkTime = now
			sequence++
			builder.WriteString(token)

			event := openaitypes.NewResponseOutputTextDeltaEvent(
				id,
				token,
				delta.Sequence,
				delta.TTFT.Milliseconds(),
				delta.ChunkLatency.Milliseconds(),
			)
			if err := writeSSEEvent(w, event.Type, event); err != nil {
				status := classifyStreamError(r)
				s.observeRoute(prepared.decision, status, ttftMs)
				s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
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

			status := classifyProviderError(r, err)
			if status == "upstream_error" {
				log.Printf("backend stream error request_id=%s backend=%s backend_type=%s model=%s err=%v", id, prepared.decision.Chosen.Name, prepared.decision.Chosen.Type, responseModel, err)
			}
			_ = writeResponsesStreamError(w, flusher, responseErrorMessage(status), errorTypeForStatus(status))
			s.observeRouteWithError(prepared.decision, status, ttftMs, err)
			s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
			return
		}
	}

	completedEvent := openaitypes.NewResponseCompletedEvent(openaitypes.NewResponse(id, responseModel, "completed", created, builder.String()))
	if err := writeSSEEvent(w, completedEvent.Type, completedEvent); err != nil {
		status := classifyStreamError(r)
		s.observeRoute(prepared.decision, status, ttftMs)
		s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, status, ttftMs, start, created)
		return
	}
	flusher.Flush()

	s.observeRoute(prepared.decision, "ok", ttftMs)
	s.recordRequest(responsesEndpoint, id, backendName, responseModel, true, "ok", ttftMs, start, created)
}

func (s *Server) collectResponsesOutput(ctx context.Context, req NormalizedRequest, decision routing.Decision, responseModel string, prepared *preparedBackendStream, start time.Time) (string, int64, string, error) {
	if prepared == nil || decision.Chosen.Client == nil {
		return "hello", time.Since(start).Milliseconds(), "ok", nil
	}

	var (
		builder       strings.Builder
		ttftMs        int64 = -1
		firstReceived       = false
	)

	tokenCh, errCh := prepared.tokenCh, prepared.errCh
	if prepared.firstToken != "" {
		ttftMs = prepared.firstTokenAt.Sub(start).Milliseconds()
		firstReceived = true
		builder.WriteString(prepared.firstToken)
	}

	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			return builder.String(), ttftMs, "client_cancel", ctx.Err()
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}
			if !firstReceived {
				ttftMs = time.Since(start).Milliseconds()
				firstReceived = true
			}
			builder.WriteString(token)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err == nil {
				continue
			}
			return builder.String(), ttftMs, responseStatusFromContext(ctx, err), err
		}
	}

	return builder.String(), ttftMs, "ok", nil
}

func (s *Server) selectExecutionTarget(w http.ResponseWriter, requestID string, requestModel string, includeRouteHeaders bool) (routing.Decision, string, string) {
	var (
		decision      routing.Decision
		backendName   = "mock"
		responseModel = requestModel
	)

	if s.router == nil || !s.router.HasBackends() {
		return decision, backendName, responseModel
	}

	decision = s.router.Choose(requestModel)
	if decision.Chosen.Name != "" {
		backendName = decision.Chosen.Name
	}
	if decision.Chosen.Client != nil {
		responseModel = decision.Chosen.Client.ResolvedModel(requestModel)
	}

	if includeRouteHeaders && s.router.Enabled() && w != nil {
		s.applyRouteHeaders(w, requestID, decision, responseModel)
	}

	return decision, backendName, responseModel
}

func parseResponsesRequest(body io.Reader) (responsesRequest, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return responsesRequest{}, fmt.Errorf("invalid request body")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return responsesRequest{}, fmt.Errorf("invalid request body")
	}

	for key := range fields {
		switch key {
		case "model", "input", "stream", "max_output_tokens", "temperature":
		default:
			return responsesRequest{}, fmt.Errorf("unsupported field %q", key)
		}
	}

	var req responsesRequest
	if raw, ok := fields["model"]; ok {
		if err := json.Unmarshal(raw, &req.Model); err != nil {
			return responsesRequest{}, fmt.Errorf("field %q must be a string", "model")
		}
	}
	if raw, ok := fields["stream"]; ok {
		if err := json.Unmarshal(raw, &req.Stream); err != nil {
			return responsesRequest{}, fmt.Errorf("field %q must be a boolean", "stream")
		}
	}
	if raw, ok := fields["max_output_tokens"]; ok {
		if err := json.Unmarshal(raw, &req.MaxOutputTokens); err != nil {
			return responsesRequest{}, fmt.Errorf("field %q must be an integer", "max_output_tokens")
		}
	}
	if raw, ok := fields["temperature"]; ok {
		if err := json.Unmarshal(raw, &req.Temperature); err != nil {
			return responsesRequest{}, fmt.Errorf("field %q must be a number", "temperature")
		}
	}

	rawInput, ok := fields["input"]
	if !ok {
		return responsesRequest{}, fmt.Errorf("field %q is required", "input")
	}

	input, err := parseResponsesInput(rawInput)
	if err != nil {
		return responsesRequest{}, err
	}
	if len(input) == 0 {
		return responsesRequest{}, fmt.Errorf("field %q must contain at least one message", "input")
	}
	req.Input = input

	return req, nil
}

func parseResponsesInput(raw json.RawMessage) ([]responsesInputItem, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("field %q must be an array of input messages", "input")
	}

	out := make([]responsesInputItem, 0, len(items))
	for index, item := range items {
		for key := range item {
			switch key {
			case "role", "content":
			default:
				return nil, fmt.Errorf("input[%d] has unsupported field %q", index, key)
			}
		}

		var role string
		if err := json.Unmarshal(item["role"], &role); err != nil {
			return nil, fmt.Errorf("input[%d].role must be a string", index)
		}
		role = strings.TrimSpace(role)
		if role == "" {
			return nil, fmt.Errorf("input[%d].role is required", index)
		}

		content, err := parseResponsesInputContent(item["content"], index)
		if err != nil {
			return nil, err
		}

		out = append(out, responsesInputItem{
			Role:    role,
			Content: content,
		})
	}

	return out, nil
}

func parseResponsesInputContent(raw json.RawMessage, index int) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", fmt.Errorf("input[%d].content is required", index)
	}

	if trimmed[0] == '"' {
		var content string
		if err := json.Unmarshal(raw, &content); err != nil {
			return "", fmt.Errorf("input[%d].content must be a string or text content array", index)
		}
		return content, nil
	}

	if trimmed[0] != '[' {
		return "", fmt.Errorf("input[%d].content must be a string or text content array", index)
	}

	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("input[%d].content must be a string or text content array", index)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("input[%d].content must contain at least one text part", index)
	}

	var builder strings.Builder
	for partIndex, part := range parts {
		for key := range part {
			switch key {
			case "type", "text":
			default:
				return "", fmt.Errorf("input[%d].content[%d] has unsupported field %q", index, partIndex, key)
			}
		}

		var decoded responsesInputTextPart
		if err := json.Unmarshal(part["type"], &decoded.Type); err != nil {
			return "", fmt.Errorf("input[%d].content[%d].type must be a string", index, partIndex)
		}
		if err := json.Unmarshal(part["text"], &decoded.Text); err != nil {
			return "", fmt.Errorf("input[%d].content[%d].text must be a string", index, partIndex)
		}

		switch decoded.Type {
		case "input_text", "text":
		default:
			return "", fmt.Errorf("input[%d].content[%d].type %q is not supported", index, partIndex, decoded.Type)
		}

		builder.WriteString(decoded.Text)
	}

	return builder.String(), nil
}

func writeSSEEvent(w http.ResponseWriter, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "event: %s\n", eventName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}

	return nil
}

func writeResponsesStreamError(w http.ResponseWriter, flusher http.Flusher, message string, errorType string) error {
	event := openaitypes.NewErrorEvent(message, errorType)
	if err := writeSSEEvent(w, event.Type, event); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) writeResponsesError(w http.ResponseWriter, statusCode int, message string, errorType string) {
	_ = writeJSON(w, statusCode, responsesErrorEnvelope{
		Error: openaitypes.ErrorDetail{
			Message: message,
			Type:    errorType,
		},
	})
}

func statusCodeForStatus(status string) int {
	switch status {
	case "bad_request":
		return http.StatusBadRequest
	case "auth_failed":
		return http.StatusUnauthorized
	case "rate_limited":
		return http.StatusTooManyRequests
	case "timeout":
		return http.StatusGatewayTimeout
	case "upstream_error":
		return http.StatusBadGateway
	case "draining":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func errorTypeForStatus(status string) string {
	switch status {
	case "bad_request":
		return "invalid_request_error"
	case "auth_failed":
		return "authentication_error"
	case "rate_limited":
		return "rate_limit_error"
	case "timeout":
		return "timeout_error"
	default:
		return "server_error"
	}
}

func responseErrorMessage(status string) string {
	switch status {
	case "auth_failed":
		return "upstream authentication failed"
	case "rate_limited":
		return "upstream rate limit exceeded"
	case "timeout":
		return "upstream request timed out"
	case "upstream_error":
		return "upstream stream failed"
	case "bad_request":
		return "upstream rejected request"
	case "draining":
		return "draining"
	case "client_cancel":
		return "client cancelled request"
	default:
		return "internal error"
	}
}

func defaultRequestModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "mock-model"
	}

	return model
}

func responseStatusFromContext(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "client_cancel"
	}

	status := statusForAdapterError(err)
	if status == "client_cancel" {
		return "upstream_error"
	}

	return status
}
