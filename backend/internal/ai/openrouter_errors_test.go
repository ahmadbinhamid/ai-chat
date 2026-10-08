package ai

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"ai-chat/internal/aicatalog"
)

// providerError gets the SDK's own error for a provider answering status and body: an HTTP failure, or with status
// 200 an SSE body carrying a mid-stream error event.
func providerError(t *testing.T, status int, body string) error {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer ts.Close()
	c := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k"), option.WithMaxRetries(0))
	stream := c.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
		Model: "m", MaxTokens: 1, Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
	defer func() { _ = stream.Close() }()
	for stream.Next() {
	}
	if stream.Err() == nil {
		t.Fatal("want an error from the fake provider")
	}
	return fmt.Errorf("provider stream: %w", stream.Err())
}

func midStreamError(errType, message string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		fmt.Sprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":%q}}\n\n", errType, message)
}

// captureErrorLogs records Error-level log messages for the rest of the test.
func captureErrorLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestOpenRouterErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		message   string
		retryable bool
		alert     string
	}{
		{
			name: "out of credits", status: http.StatusPaymentRequired,
			body:    `{"type":"error","error":{"type":"billing_error","message":"Insufficient credits. Add more using https://openrouter.ai/settings/credits"}}`,
			message: "the AI service is temporarily unavailable", alert: "out of credits",
		},
		{
			name: "max_tokens unaffordable", status: http.StatusPaymentRequired,
			body:    `{"type":"error","error":{"type":"invalid_request_error","message":"This request requires more credits, or fewer max_tokens. You requested up to 64000 tokens, but can only afford 1200."}}`,
			message: "the AI service is temporarily unavailable", alert: "out of credits",
		},
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			body:    `{"type":"error","error":{"type":"rate_limit_error","message":"Provider returned error","error_type":"rate_limit_exceeded"}}`,
			message: "too many requests", retryable: true,
		},
		{
			name: "rate limited mid-stream", status: http.StatusOK, body: midStreamError("rate_limit_error", "Provider returned error"),
			message: "too many requests", retryable: true,
		},
		{
			name: "upstream host failed", status: http.StatusBadGateway,
			body:    `{"type":"error","error":{"type":"api_error","message":"Provider returned error"}}`,
			message: "temporarily unavailable", retryable: true,
		},
		{
			name: "upstream host failed mid-stream", status: http.StatusOK, body: midStreamError("api_error", "Provider returned error"),
			message: "temporarily unavailable", retryable: true,
		},
		{
			name: "upstream overloaded mid-stream", status: http.StatusOK, body: midStreamError("overloaded_error", "Overloaded"),
			message: "temporarily unavailable", retryable: true,
		},
		{
			name: "unknown model", status: http.StatusBadRequest,
			body:    `{"type":"error","error":{"type":"invalid_request_error","message":"deepseek/deepseek-v9 is not a valid model ID"}}`,
			message: "the AI service is temporarily unavailable", alert: "doesn't know a catalogue model",
		},
		{
			name: "no host can serve it", status: http.StatusNotFound,
			body:    `{"type":"error","error":{"type":"not_found_error","message":"No endpoints found for deepseek/deepseek-v4-pro. Every candidate endpoint was removed during routing"}}`,
			message: "the AI service is temporarily unavailable", alert: "no host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureErrorLogs(t)
			err := providerError(t, tt.status, tt.body)
			msg := SanitizeError(err)
			if !strings.Contains(msg, tt.message) {
				t.Errorf("SanitizeError = %q, want it to contain %q", msg, tt.message)
			}
			for _, leak := range []string{"openrouter", "OpenRouter", "deepseek", "Provider returned"} {
				if strings.Contains(msg, leak) {
					t.Errorf("merchant message leaks %q: %q", leak, msg)
				}
			}
			if got := isRetryableStreamErr(err); got != tt.retryable {
				t.Errorf("retryable = %v, want %v", got, tt.retryable)
			}
			alertProviderError(err, "deepseek/deepseek-v4-pro")
			if got := logs(); tt.alert == "" && got != "" {
				t.Errorf("want no Error log, got %s", got)
			} else if tt.alert != "" && !strings.Contains(got, tt.alert) {
				t.Errorf("want an Error log containing %q, got %q", tt.alert, got)
			}
		})
	}
}

// A failed upstream host goes back through the call's existing retry, and the next attempt succeeds.
func TestGenerate_RetriesAFailedUpstreamHost(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, midStreamError("api_error", "Provider returned error"))
			return
		}
		fmt.Fprint(w, toolUseSSEResponse("msg_2", "toolu_2", toolNameProposeChanges, emptyAnswer("Done."), 10, 5))
	}))
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}}, nil, "x", nil, nil, nil, nil); err != nil {
		t.Fatalf("want the retry to succeed, got %v", err)
	}
	if calls != 2 {
		t.Errorf("want 2 attempts, got %d", calls)
	}
}

// Out of credits is never retried: it fails the same way every time, and the operator is alerted.
func TestGenerate_DoesNotRetryOutOfCredits(t *testing.T) {
	logs := captureErrorLogs(t)
	var mu sync.Mutex
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"type":"error","error":{"type":"billing_error","message":"Insufficient credits"}}`)
	}))
	defer ts.Close()
	g := catalogueGenerator(t, client(ts.URL), nil)
	_, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "s", Model: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}}, nil, "x", nil, nil, nil, nil)
	if err == nil || !strings.Contains(SanitizeError(err), "temporarily unavailable") {
		t.Fatalf("want the unavailable message, got %v", err)
	}
	if calls != 1 {
		t.Errorf("want 1 attempt, got %d", calls)
	}
	if !strings.Contains(logs(), "out of credits") {
		t.Errorf("want an Error log, got %q", logs())
	}
}
