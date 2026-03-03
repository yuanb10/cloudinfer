package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/myusername/cloudinfer/internal/adapters"
	adapteropenai "github.com/myusername/cloudinfer/internal/adapters/openai"
	adaptervertex "github.com/myusername/cloudinfer/internal/adapters/vertex"
	"github.com/myusername/cloudinfer/internal/config"
)

func TestAdapterConformance(t *testing.T) {
	t.Run("streaming ordering", func(t *testing.T) {
		for _, harness := range conformanceHarnesses(t) {
			t.Run(harness.name, func(t *testing.T) {
				adapter, cleanup := harness.newOrdered(t)
				defer cleanup()

				stream, meta, err := adapter.Generate(context.Background(), testRequest(true))
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
				defer func() { _ = stream.Close() }()

				if meta.Model == "" {
					t.Fatal("meta.Model is empty")
				}
				if meta.StreamMode != adapters.StreamModeStream {
					t.Fatalf("meta.StreamMode = %q, want %q", meta.StreamMode, adapters.StreamModeStream)
				}

				if got := collectStream(t, stream); got != "abc" {
					t.Fatalf("stream output = %q, want %q", got, "abc")
				}
			})
		}
	})

	t.Run("cancellation propagation", func(t *testing.T) {
		for _, harness := range conformanceHarnesses(t) {
			t.Run(harness.name, func(t *testing.T) {
				adapter, cleanup, started := harness.newBlocking(t)
				defer cleanup()

				ctx, cancel := context.WithCancel(context.Background())
				stream, _, err := adapter.Generate(ctx, testRequest(true))
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
				defer func() { _ = stream.Close() }()

				<-started
				cancel()

				_, recvErr := waitRecv(t, stream)
				if !errors.Is(recvErr, context.Canceled) {
					t.Fatalf("Recv() error = %v, want context.Canceled", recvErr)
				}
			})
		}
	})

	t.Run("timeout enforcement", func(t *testing.T) {
		for _, harness := range conformanceHarnesses(t) {
			t.Run(harness.name, func(t *testing.T) {
				adapter, cleanup := harness.newTimeout(t)
				defer cleanup()

				err := terminalError(t, adapter)
				var adapterErr *adapters.Error
				if !errors.As(err, &adapterErr) {
					t.Fatalf("terminal error = %v, want *adapters.Error", err)
				}
				if adapterErr.Category != adapters.CategoryTimeout {
					t.Fatalf("error category = %q, want %q", adapterErr.Category, adapters.CategoryTimeout)
				}
			})
		}
	})

	t.Run("error normalization", func(t *testing.T) {
		for _, harness := range conformanceHarnesses(t) {
			t.Run(harness.name, func(t *testing.T) {
				adapter, cleanup := harness.newRateLimited(t)
				defer cleanup()

				err := terminalError(t, adapter)
				var adapterErr *adapters.Error
				if !errors.As(err, &adapterErr) {
					t.Fatalf("terminal error = %v, want *adapters.Error", err)
				}
				if adapterErr.Category != adapters.CategoryRateLimited {
					t.Fatalf("error category = %q, want %q", adapterErr.Category, adapters.CategoryRateLimited)
				}
				if harness.name == "openai-compatible" && adapterErr.RetryAfter != 3*time.Second {
					t.Fatalf("retry_after = %s, want %s", adapterErr.RetryAfter, 3*time.Second)
				}
			})
		}
	})
}

func TestVertexAdapterNonStreamFallback(t *testing.T) {
	adapter := adaptervertex.NewWithClient("gemini-2.0-flash", fakeVertexClient{
		generateContent: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			return textResponse("fallback"), nil
		},
		generateContentStream: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
			return func(func(*genai.GenerateContentResponse, error) bool) {}
		},
	})

	stream, meta, err := adapter.Generate(context.Background(), testRequest(false))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	defer func() { _ = stream.Close() }()

	if meta.StreamMode != adapters.StreamModeSingle {
		t.Fatalf("meta.StreamMode = %q, want %q", meta.StreamMode, adapters.StreamModeSingle)
	}
	if got := collectStream(t, stream); got != "fallback" {
		t.Fatalf("stream output = %q, want %q", got, "fallback")
	}
}

type harness struct {
	name           string
	newOrdered     func(*testing.T) (adapters.Adapter, func())
	newBlocking    func(*testing.T) (adapters.Adapter, func(), <-chan struct{})
	newTimeout     func(*testing.T) (adapters.Adapter, func())
	newRateLimited func(*testing.T) (adapters.Adapter, func())
}

func conformanceHarnesses(t *testing.T) []harness {
	t.Helper()

	return []harness{
		newOpenAIHarness(t),
		newVertexHarness(),
	}
}

func newOpenAIHarness(t *testing.T) harness {
	t.Helper()

	return harness{
		name: "openai-compatible",
		newOrdered: func(t *testing.T) (adapters.Adapter, func()) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/responses")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprint(w, "event: response.output_text.delta\n")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n")
				flusher.Flush()
				fmt.Fprint(w, "event: response.output_text.delta\n")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"b\"}\n\n")
				flusher.Flush()
				fmt.Fprint(w, "event: response.output_text.delta\n")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"c\"}\n\n")
				flusher.Flush()
				fmt.Fprint(w, "event: response.completed\n")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
				flusher.Flush()
			}))
			return newOpenAIAdapter(t, server.Client(), server.URL), server.Close
		},
		newBlocking: func(t *testing.T) (adapters.Adapter, func(), <-chan struct{}) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
				close(started)
				<-r.Context().Done()
				time.Sleep(25 * time.Millisecond)
			}))
			return newOpenAIAdapter(t, server.Client(), server.URL), server.Close, started
		},
		newTimeout: func(t *testing.T) (adapters.Adapter, func()) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(200 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
			}))
			client := server.Client()
			client.Timeout = 50 * time.Millisecond
			return newOpenAIAdapter(t, client, server.URL), server.Close
		},
		newRateLimited: func(t *testing.T) (adapters.Adapter, func()) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
			}))
			return newOpenAIAdapter(t, server.Client(), server.URL), server.Close
		},
	}
}

func newVertexHarness() harness {
	return harness{
		name: "vertex-adc",
		newOrdered: func(*testing.T) (adapters.Adapter, func()) {
			adapter := adaptervertex.NewWithClient("gemini-2.0-flash", fakeVertexClient{
				generateContentStream: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
					return func(yield func(*genai.GenerateContentResponse, error) bool) {
						if !yield(textResponse("a"), nil) {
							return
						}
						if !yield(textResponse("ab"), nil) {
							return
						}
						yield(textResponse("abc"), nil)
					}
				},
			})
			return adapter, func() {}
		},
		newBlocking: func(*testing.T) (adapters.Adapter, func(), <-chan struct{}) {
			started := make(chan struct{})
			adapter := adaptervertex.NewWithClient("gemini-2.0-flash", fakeVertexClient{
				generateContentStream: func(ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
					return func(yield func(*genai.GenerateContentResponse, error) bool) {
						close(started)
						<-ctx.Done()
						yield(nil, ctx.Err())
					}
				},
			})
			return adapter, func() {}, started
		},
		newTimeout: func(*testing.T) (adapters.Adapter, func()) {
			adapter := adaptervertex.NewWithClient("gemini-2.0-flash", fakeVertexClient{
				generateContentStream: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
					return func(yield func(*genai.GenerateContentResponse, error) bool) {
						yield(nil, context.DeadlineExceeded)
					}
				},
			})
			return adapter, func() {}
		},
		newRateLimited: func(*testing.T) (adapters.Adapter, func()) {
			adapter := adaptervertex.NewWithClient("gemini-2.0-flash", fakeVertexClient{
				generateContentStream: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
					return func(yield func(*genai.GenerateContentResponse, error) bool) {
						yield(nil, genai.APIError{Code: 429, Message: "quota exceeded"})
					}
				},
			})
			return adapter, func() {}
		},
	}
}

func newOpenAIAdapter(t *testing.T, client *http.Client, baseURL string) adapters.Adapter {
	t.Helper()

	t.Setenv("OPENAI_API_KEY", "test-key")
	adapter, err := adapteropenai.NewWithClient(config.OpenAIConfig{
		APIKeyEnv: "OPENAI_API_KEY",
		BaseURL:   baseURL,
		Model:     "gpt-4o-mini",
	}, client)
	if err != nil {
		t.Fatalf("NewWithClient() error = %v", err)
	}
	return adapter
}

func testRequest(stream bool) adapters.NormalizedRequest {
	return adapters.NormalizedRequest{
		Model:  "default",
		Stream: stream,
		Input: []adapters.NormalizedInputItem{
			{Role: "user", Content: "hello"},
		},
	}
}

func collectStream(t *testing.T, stream adapters.Stream) string {
	t.Helper()

	var builder strings.Builder
	for {
		delta, err := waitRecv(t, stream)
		if err == nil {
			builder.WriteString(delta.Text)
			continue
		}
		if err == io.EOF {
			return builder.String()
		}
		t.Fatalf("Recv() error = %v", err)
	}
}

func waitRecv(t *testing.T, stream adapters.Stream) (adapters.Delta, error) {
	t.Helper()

	type recvResult struct {
		delta adapters.Delta
		err   error
	}

	resultCh := make(chan recvResult, 1)
	go func() {
		delta, err := stream.Recv()
		resultCh <- recvResult{delta: delta, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.delta, result.err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream recv")
		return adapters.Delta{}, nil
	}
}

func terminalError(t *testing.T, adapter adapters.Adapter) error {
	t.Helper()

	stream, _, err := adapter.Generate(context.Background(), testRequest(true))
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	for {
		_, recvErr := waitRecv(t, stream)
		if recvErr == nil {
			continue
		}
		if recvErr == io.EOF {
			t.Fatal("stream completed without terminal error")
		}
		return recvErr
	}
}

type fakeVertexClient struct {
	generateContent       func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	generateContentStream func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
}

func (c fakeVertexClient) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if c.generateContent == nil {
		return nil, errors.New("unexpected GenerateContent call")
	}
	return c.generateContent(ctx, model, contents, config)
}

func (c fakeVertexClient) GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	if c.generateContentStream == nil {
		return func(func(*genai.GenerateContentResponse, error) bool) {}
	}
	return c.generateContentStream(ctx, model, contents, config)
}

func (c fakeVertexClient) Close() error {
	return nil
}

func textResponse(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: genai.NewContentFromText(text, genai.RoleModel),
			},
		},
	}
}
