package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if !isRetryableStreamErr(errStreamIdleTimeout) {
		t.Fatal("idle should be retryable")
	}
	if !isRetryableStreamErr(errStreamTruncated) {
		t.Fatal("truncated should be retryable")
	}
	if isRetryableStreamErr(context.Canceled) {
		t.Fatal("canceled should not be retryable")
	}
}
