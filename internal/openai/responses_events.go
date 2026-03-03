package openai

import "strings"

type Response struct {
	ID         string               `json:"id"`
	Object     string               `json:"object"`
	CreatedAt  int64                `json:"created_at"`
	Status     string               `json:"status"`
	Model      string               `json:"model"`
	Output     []ResponseOutputItem `json:"output,omitempty"`
	OutputText string               `json:"output_text,omitempty"`
}

type ResponseOutputItem struct {
	ID      string                `json:"id"`
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Content []ResponseContentPart `json:"content"`
}

type ResponseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ResponseCreatedEvent struct {
	Type     string   `json:"type"`
	Response Response `json:"response"`
}

type ResponseOutputTextDeltaEvent struct {
	Type           string        `json:"type"`
	ResponseID     string        `json:"response_id"`
	OutputIndex    int           `json:"output_index"`
	ContentIndex   int           `json:"content_index"`
	SequenceNumber int           `json:"sequence_number"`
	Delta          string        `json:"delta"`
	Metrics        *DeltaMetrics `json:"metrics,omitempty"`
}

type DeltaMetrics struct {
	TTFTms  int64 `json:"ttft_ms,omitempty"`
	ChunkMs int64 `json:"chunk_ms,omitempty"`
}

type ResponseCompletedEvent struct {
	Type     string   `json:"type"`
	Response Response `json:"response"`
}

type ErrorEvent struct {
	Type  string      `json:"type"`
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func NewResponse(id string, model string, status string, createdAt int64, outputText string) Response {
	resp := Response{
		ID:        id,
		Object:    "response",
		CreatedAt: createdAt,
		Status:    status,
		Model:     model,
	}

	if outputText == "" {
		return resp
	}

	resp.Output = []ResponseOutputItem{
		{
			ID:   outputMessageID(id),
			Type: "message",
			Role: "assistant",
			Content: []ResponseContentPart{
				{
					Type: "output_text",
					Text: outputText,
				},
			},
		},
	}
	resp.OutputText = outputText

	return resp
}

func NewResponseCreatedEvent(response Response) ResponseCreatedEvent {
	return ResponseCreatedEvent{
		Type:     "response.created",
		Response: response,
	}
}

func NewResponseOutputTextDeltaEvent(responseID string, delta string, sequenceNumber int, ttftMs int64, chunkMs int64) ResponseOutputTextDeltaEvent {
	event := ResponseOutputTextDeltaEvent{
		Type:           "response.output_text.delta",
		ResponseID:     responseID,
		OutputIndex:    0,
		ContentIndex:   0,
		SequenceNumber: sequenceNumber,
		Delta:          delta,
	}

	if ttftMs > 0 || chunkMs > 0 {
		event.Metrics = &DeltaMetrics{
			TTFTms:  ttftMs,
			ChunkMs: chunkMs,
		}
	}

	return event
}

func NewResponseCompletedEvent(response Response) ResponseCompletedEvent {
	return ResponseCompletedEvent{
		Type:     "response.completed",
		Response: response,
	}
}

func NewErrorEvent(message string, errorType string) ErrorEvent {
	return ErrorEvent{
		Type: "error",
		Error: ErrorDetail{
			Message: message,
			Type:    errorType,
		},
	}
}

func outputMessageID(responseID string) string {
	if trimmed := strings.TrimPrefix(responseID, "resp_"); trimmed != responseID {
		return "msg_" + trimmed
	}

	return "msg_" + responseID
}
