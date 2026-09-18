package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"errors"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestGenerate_StreamIdleTimeoutRetriesOnce verifies a hung SSE stream is
// cancelled by the idle deadline and retried at most once (not left running
// for minutes).
func TestGenerate_StreamIdleTimeoutRetriesOnce(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if calls == 1 {
			// Start a message then stall — idle watchdog must cancel.
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stall\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(3 * time.Second)
			return
		}
		fmt.Fprint(w, toolUseSSEResponse("msg_ok", "toolu_1", "propose_changes", map[string]any{
			"summary":               "Done with a small header tweak.",
			"needs_clarification":   false,
			"answered_question":     false,
			"files":                 []map[string]any{},
			"page_registry_entry":   nil,
			"layout_links_to_add":   []string{},
			"layout_scripts_to_add": []string{},
		}, 20, 10))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	g.SetStreamIdleTimeout(400 * time.Millisecond)

	start := time.Now()
	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo", MaxToolIterations: 4}, nil,
		"change the header", nil, nil, nil, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil || !strings.Contains(result.Summary, "header") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if calls != 2 {
		t.Fatalf("expected 2 stream attempts (idle fail + retry), got %d", calls)
	}
	// Must not approach the old ~167s stall — idle 400ms + 1s retry delay + success.
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed %s too long — idle timeout not bounding stall", elapsed)
	}
}

func TestIsRetryableStreamErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"idle", errStreamIdleTimeout, true},
		{"first_token", errStreamFirstTokenTimeout, true},
		{"truncated", errStreamTruncated, true},
		{"accumulate_msg", errors.New("accumulate stream: boom"), true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"401", errors.New("API status 401 unauthorized"), false},
		{"403", errors.New("403 Forbidden"), false},
		{"invalid_request", errors.New("invalid_request_error: bad schema"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableStreamErr(tt.err); got != tt.want {
				t.Fatalf("got %v want %v for %v", got, tt.want, tt.err)
			}
		})
	}
}

// TestGenerate_StreamFirstTokenTimeoutCancelsWithoutFreshRetry verifies a
// stream that never yields content tokens fails on the first-token deadline
// and does not start a second attempt with a fresh full TTFT budget.
func TestGenerate_StreamFirstTokenTimeoutCancelsWithoutFreshRetry(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Control frame only — no thinking/text content. Idle is long;
		// first-token deadline must cancel; shared budget leaves no retry.
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ft\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(3 * time.Second)
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	g.SetStreamIdleTimeout(10 * time.Second) // must not win before first-token
	g.SetStreamFirstTokenTimeout(400 * time.Millisecond)

	start := time.Now()
	_, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo", MaxToolIterations: 4}, nil,
		"change the header", nil, nil, nil, nil, nil)
	elapsed := time.Since(start)
	if err == nil || !errors.Is(err, errStreamFirstTokenTimeout) {
		t.Fatalf("expected first-token timeout, got err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 stream attempt (shared TTFT budget exhausted), got %d", calls)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed %s too long — first-token timeout not bounding stall", elapsed)
	}
}
