package openai

import "testing"

func TestNewResponseCreatedEvent(t *testing.T) {
	response := NewResponse("resp_123", "gpt-4.1-mini", "in_progress", 123, "")
	event := NewResponseCreatedEvent(response)

	if event.Type != "response.created" {
		t.Fatalf("type = %q, want %q", event.Type, "response.created")
	}
	if event.Response.ID != "resp_123" {
		t.Fatalf("response id = %q, want %q", event.Response.ID, "resp_123")
	}
	if len(event.Response.Output) != 0 {
		t.Fatalf("output length = %d, want 0", len(event.Response.Output))
	}
}

func TestNewResponseOutputTextDeltaEventIncludesMetrics(t *testing.T) {
	event := NewResponseOutputTextDeltaEvent("resp_123", "he", 2, 45, 45)

	if event.Type != "response.output_text.delta" {
		t.Fatalf("type = %q, want %q", event.Type, "response.output_text.delta")
	}
	if event.SequenceNumber != 2 {
		t.Fatalf("sequence_number = %d, want %d", event.SequenceNumber, 2)
	}
	if event.Metrics == nil {
		t.Fatal("metrics are missing")
	}
	if event.Metrics.TTFTms != 45 {
		t.Fatalf("ttft_ms = %d, want %d", event.Metrics.TTFTms, 45)
	}
	if event.Metrics.ChunkMs != 45 {
		t.Fatalf("chunk_ms = %d, want %d", event.Metrics.ChunkMs, 45)
	}
}

func TestNewResponseCompletedEventIncludesOutputText(t *testing.T) {
	response := NewResponse("resp_123", "gpt-4.1-mini", "completed", 123, "hello")
	event := NewResponseCompletedEvent(response)

	if event.Type != "response.completed" {
		t.Fatalf("type = %q, want %q", event.Type, "response.completed")
	}
	if event.Response.OutputText != "hello" {
		t.Fatalf("output_text = %q, want %q", event.Response.OutputText, "hello")
	}
	if len(event.Response.Output) != 1 {
		t.Fatalf("output length = %d, want 1", len(event.Response.Output))
	}
	if event.Response.Output[0].ID != "msg_123" {
		t.Fatalf("output[0].id = %q, want %q", event.Response.Output[0].ID, "msg_123")
	}
}
