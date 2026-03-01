package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/myusername/cloudinfer/internal/config"
)

func TestAdapterStreamText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dummy" {
			t.Fatalf("authorization = %q, want %q", got, "Bearer dummy")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "dummy")

	adapter, err := New(config.OpenAIConfig{
		APIKeyEnv: "OPENAI_API_KEY",
		BaseURL:   server.URL + "/v1",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	tokenCh, errCh := adapter.StreamText(context.Background(), "", []Message{{Role: "user", Content: "hello"}})
	got := collectTokens(t, tokenCh, errCh)
	if got != "Hello" {
		t.Fatalf("tokens = %q, want %q", got, "Hello")
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "https://api.openai.com/v1"},
		{name: "already v1", in: "https://example.com/v1", want: "https://example.com/v1"},
		{name: "trailing slash", in: "https://example.com/v1/", want: "https://example.com/v1"},
		{name: "append v1", in: "https://example.com", want: "https://example.com/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeBaseURL(tt.in); got != tt.want {
				t.Fatalf("normalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAdapterBaseURLWithoutV1CallsNormalizedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "dummy")

	adapter, err := New(config.OpenAIConfig{
		APIKeyEnv: "OPENAI_API_KEY",
		BaseURL:   server.URL,
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	tokenCh, errCh := adapter.StreamText(context.Background(), "", []Message{{Role: "user", Content: "hello"}})
	got := collectTokens(t, tokenCh, errCh)
	if got != "" {
		t.Fatalf("tokens = %q, want empty", got)
	}
}

func TestAdapterNon200ReturnsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "dummy")

	adapter, err := New(config.OpenAIConfig{
		APIKeyEnv: "OPENAI_API_KEY",
		BaseURL:   server.URL,
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	tokenCh, errCh := adapter.StreamText(context.Background(), "", []Message{{Role: "user", Content: "hello"}})
	if got := collectTokensUntilError(t, tokenCh, errCh); !strings.Contains(got, "provider_error: status=502") {
		t.Fatalf("error = %q, want provider_error status", got)
	}
}

func TestNewRequiresAPIKeyAtRuntime(t *testing.T) {
	envName := "TEST_OPENAI_API_KEY_MISSING"
	_ = os.Unsetenv(envName)

	_, err := New(config.OpenAIConfig{
		APIKeyEnv: envName,
	})
	if err == nil {
		t.Fatal("New() error = nil, want missing api key error")
	}
}

func collectTokens(t *testing.T, tokenCh <-chan string, errCh <-chan error) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var builder strings.Builder
	for tokenCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for stream")
		case token, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
				continue
			}
			builder.WriteString(token)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
	}

	return builder.String()
}

func collectTokensUntilError(t *testing.T, tokenCh <-chan string, errCh <-chan error) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for error")
		case _, ok := <-tokenCh:
			if !ok {
				tokenCh = nil
			}
		case err, ok := <-errCh:
			if !ok {
				t.Fatal("stream closed without error")
			}
			if err == nil {
				continue
			}
			return fmt.Sprint(err)
		}
	}
}
