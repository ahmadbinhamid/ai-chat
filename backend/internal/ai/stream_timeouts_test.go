package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// newStreamingTestStream opens a real *ssestream.Stream against ts; needed because
// consumeStream takes the SDK's stream type directly, not an interface.
func newStreamingTestStream(ts *httptest.Server) *ssestream.Stream[anthropic.MessageStreamEventUnion] {
	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	return client.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
		Model:     "test-model",
		MaxTokens: 1024,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
}

// flush writes s and flushes immediately, so a test server can send a partial SSE
// response and stall without buffering until the handler returns.
func flush(w http.ResponseWriter, s string) {
	fmt.Fprint(w, s)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestConsumeStream_IdleTimeoutFiresOnStalledConnection checks a connection that goes quiet
// after producing at least one event is treated as hung, returned as errStreamIdle.
func TestConsumeStream_IdleTimeoutFiresOnStalledConnection(t *testing.T) {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	firstEvent := b.String()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, firstEvent)
		// Deliberately never send anything else — "stalled but still connected".
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message

	start := time.Now()
	err := consumeStream(context.Background(), stream, &message, 50*time.Millisecond, time.Second)
	elapsed := time.Since(start)
	_ = stream.Close()

	if !errors.Is(err, errStreamIdle) {
		t.Fatalf("expected errStreamIdle, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("idle timeout took %v, expected roughly the configured 50ms", elapsed)
	}
}

// TestConsumeStream_FirstTokenTimeoutFiresWhenNothingEverArrives checks a connection that
// never produces one byte of real content fails on firstTokenTimeout, shorter than idle here.
func TestConsumeStream_FirstTokenTimeoutFiresWhenNothingEverArrives(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, "")
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message

	err := consumeStream(context.Background(), stream, &message, time.Second, 50*time.Millisecond)
	_ = stream.Close()

	if !errors.Is(err, errStreamFirstToken) {
		t.Fatalf("expected errStreamFirstToken, got %v", err)
	}
}

// TestConsumeStream_SlowFirstEventWithinFirstTokenBudgetSucceeds checks a stream whose first event arrives after
// longer than idleTimeout, but within firstTokenTimeout, isn't killed as idle: a router sends only SSE comments
// (dropped by the SDK) while the upstream host queues and prefills.
func TestConsumeStream_SlowFirstEventWithinFirstTokenBudgetSucceeds(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, ": OPENROUTER PROCESSING\n\n")
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		flush(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", emptyAnswer("Done."), 10, 5))
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message

	err := consumeStream(context.Background(), stream, &message, 50*time.Millisecond, time.Second)
	_ = stream.Close()

	if err != nil {
		t.Fatalf("expected the slow first event to be waited for, got %v", err)
	}
	if message.StopReason != anthropic.StopReasonToolUse || len(message.Content) != 1 {
		t.Fatalf("expected the full message once events flowed, got %+v", message)
	}
}

// TestConsumeStream_NothingAtAllFailsOnFirstTokenNotIdle checks a stream that never sends an event fails on the
// first-token budget even when idleTimeout is shorter: the idle rule only applies once events have started.
func TestConsumeStream_NothingAtAllFailsOnFirstTokenNotIdle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, "")
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message

	start := time.Now()
	err := consumeStream(context.Background(), stream, &message, 50*time.Millisecond, 200*time.Millisecond)
	elapsed := time.Since(start)
	_ = stream.Close()

	if !errors.Is(err, errStreamFirstToken) {
		t.Fatalf("expected errStreamFirstToken, got %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("failed after %v, before the 200ms first-token budget", elapsed)
	}
}

// TestConsumeStream_ToolUseBytesCountAsFirstTokenProgress checks tool_use-only streaming
func TestConsumeStream_ToolUseBytesCountAsFirstTokenProgress(t *testing.T) {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	sseEvent(&b, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "propose_changes", "input": map[string]any{}},
	})
	sseEvent(&b, "content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"summary":"a large proposal in progress"}`},
	})
	events := b.String()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, events)
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message

	// firstToken shorter than idle: if tool_use bytes didn't count as progress, this
	// would return errStreamFirstToken at ~40ms instead.
	err := consumeStream(context.Background(), stream, &message, 150*time.Millisecond, 40*time.Millisecond)
	_ = stream.Close()

	if !errors.Is(err, errStreamIdle) {
		t.Fatalf("expected the idle timeout to fire (proving tool_use content counted as first-token progress), got %v", err)
	}
}

// TestConsumeStream_DeltasCountAsFirstTokenProgress checks thinking/text arriving only as deltas on
// a still-open block counts as progress — a long think must not be killed as "no first token".
func TestConsumeStream_DeltasCountAsFirstTokenProgress(t *testing.T) {
	cases := []struct {
		name  string
		start map[string]any
		delta map[string]any
	}{
		{
			name:  "thinking",
			start: map[string]any{"type": "thinking", "thinking": "", "signature": ""},
			delta: map[string]any{"type": "thinking_delta", "thinking": "planning the redesign"},
		},
		{
			name:  "text",
			start: map[string]any{"type": "text", "text": ""},
			delta: map[string]any{"type": "text_delta", "text": "Here is the plan"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			sseEvent(&b, "message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
					"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
				},
			})
			sseEvent(&b, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": c.start})
			sseEvent(&b, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": c.delta})
			events := b.String()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flush(w, events)
				<-r.Context().Done()
			}))
			defer ts.Close()

			stream := newStreamingTestStream(ts)
			var message anthropic.Message

			err := consumeStream(context.Background(), stream, &message, 150*time.Millisecond, 40*time.Millisecond)
			_ = stream.Close()

			if !errors.Is(err, errStreamIdle) {
				t.Fatalf("expected errStreamIdle (delta counted as first-token progress), got %v", err)
			}
		})
	}
}

// TestGenerate_RetriesOnIdleTimeout checks a stalled first attempt is retried, ending in a
// successful Result once a later attempt completes normally.
func TestGenerate_RetriesOnIdleTimeout(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			flush(w, "")
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", map[string]any{
			"summary": "done", "needs_clarification": false,
			"files": []map[string]any{}, "page_registry_entry": nil,
			"layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
		}, 10, 5))
	}))
	defer ts.Close()

	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	g := newTestGenerator(client)
	g.streamTimeouts.Idle = 50 * time.Millisecond
	g.streamTimeouts.FirstTokenEdit = time.Second

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "hello", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate returned an error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls to the fake server (1 stalled attempt + 1 retry), got %d", calls)
	}
	if result.Summary != "done" {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
}

// TestStreamProgressBytes_CountsTextThinkingAndToolUse covers every branch streamProgressBytes
func TestStreamProgressBytes_CountsTextThinkingAndToolUse(t *testing.T) {
	var blocks []anthropic.ContentBlockUnion
	raw := `[
		{"type":"text","text":"hello"},
		{"type":"thinking","thinking":"reasoning"},
		{"type":"tool_use","id":"toolu_1","name":"ab","input":{"x":1}}
	]`
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("unmarshal fixture content blocks: %v", err)
	}
	message := anthropic.Message{Content: blocks}

	got := streamProgressBytes(message)
	want := len("hello") + len("reasoning") + len("ab") + len(`{"x":1}`)
	if got != want {
		t.Fatalf("streamProgressBytes = %d, want %d", got, want)
	}
}

// TestStreamProgressBytes_EmptyMessageIsZero guards the "no progress yet" baseline.
func TestStreamProgressBytes_EmptyMessageIsZero(t *testing.T) {
	if got := streamProgressBytes(anthropic.Message{}); got != 0 {
		t.Fatalf("expected 0 for an empty message, got %d", got)
	}
}

// TestClampToContextDeadline_ShortensWhenParentDeadlineIsSooner checks a first-token budget
// never outlives the generation it's part of.
func TestClampToContextDeadline_ShortensWhenParentDeadlineIsSooner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	got := clampToContextDeadline(ctx, time.Hour)
	if got <= 0 || got > 20*time.Millisecond {
		t.Fatalf("expected a duration clamped to roughly the 20ms remaining on ctx, got %v", got)
	}
}

// TestClampToContextDeadline_LeavesUnchangedWithNoDeadline checks clamping with no parent
// deadline is a no-op, not a zero timeout.
func TestClampToContextDeadline_LeavesUnchangedWithNoDeadline(t *testing.T) {
	got := clampToContextDeadline(context.Background(), 45*time.Second)
	if got != 45*time.Second {
		t.Fatalf("expected d unchanged with no ctx deadline, got %v", got)
	}
}

// TestClampToContextDeadline_NeverGoesNegative checks an already-expired deadline returns
// 0, not a negative duration that would make time.NewTimer panic.
func TestClampToContextDeadline_NeverGoesNegative(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if got := clampToContextDeadline(ctx, time.Minute); got != 0 {
		t.Fatalf("expected 0 for an already-expired deadline, got %v", got)
	}
}

// TestIsRetryableStreamErr_ExcludesContextCancellation checks ctx.Err() is never treated
// as a retryable provider hiccup — retrying it would spend the delay against a dead context.
func TestIsRetryableStreamErr_ExcludesContextCancellation(t *testing.T) {
	if isRetryableStreamErr(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must not be retryable")
	}
	if isRetryableStreamErr(context.Canceled) {
		t.Error("context.Canceled must not be retryable")
	}
	if !isRetryableStreamErr(errStreamIdle) {
		t.Error("errStreamIdle must be retryable")
	}
	if !isRetryableStreamErr(errStreamFirstToken) {
		t.Error("errStreamFirstToken must be retryable")
	}
	if !isRetryableStreamErr(fmt.Errorf("accumulate stream: %w", errors.New("boom"))) {
		t.Error("a wrapped accumulate error must still be retryable")
	}
}

// TestGenerator_FirstTokenTimeoutFor_MapsGenerationModes covers StreamTimeouts' per-mode
// mapping and its zero-value fallback to package defaults.
func TestGenerator_FirstTokenTimeoutFor_MapsGenerationModes(t *testing.T) {
	g := &Generator{streamTimeouts: StreamTimeouts{
		FirstTokenEdit:  111 * time.Second,
		FirstTokenBrand: 22 * time.Second,
		FirstTokenCopy:  33 * time.Second,
		FirstTokenPages: 444 * time.Second,
	}}

	cases := []struct {
		mode string
		want time.Duration
	}{
		{GenerationModeEdit, 111 * time.Second},
		{"", 111 * time.Second}, // unset Mode defaults to edit's budget
		{GenerationModeBrand, 22 * time.Second},
		{GenerationModeCopy, 33 * time.Second},
		{GenerationModePages, 444 * time.Second},
	}
	for _, c := range cases {
		if got := g.firstTokenTimeoutFor(c.mode); got != c.want {
			t.Errorf("firstTokenTimeoutFor(%q) = %v, want %v", c.mode, got, c.want)
		}
	}
}

// TestGenerator_FirstTokenTimeoutFor_DefaultsWhenZero checks a Generator built without New
// (a bare struct literal) falls back to package defaults, never an instant/zero timeout.
func TestGenerator_FirstTokenTimeoutFor_DefaultsWhenZero(t *testing.T) {
	var g Generator
	if got := g.firstTokenTimeoutFor(GenerationModeEdit); got != defaultFirstTokenTimeoutEdit {
		t.Errorf("edit: got %v, want default %v", got, defaultFirstTokenTimeoutEdit)
	}
	if got := g.firstTokenTimeoutFor(GenerationModeBrand); got != defaultFirstTokenTimeoutNarrow {
		t.Errorf("brand: got %v, want default %v", got, defaultFirstTokenTimeoutNarrow)
	}
	if got := g.idleTimeout(); got != defaultStreamIdleTimeout {
		t.Errorf("idle: got %v, want default %v", got, defaultStreamIdleTimeout)
	}
}

// messageStartOnly is the event a router can send before its upstream host has read the prompt.
func messageStartOnly() string {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	return b.String()
}

// With idleAfterContent, silence between message_start and the first real output is prefill, not a stall, so only the
// first-token budget applies to it; once output flows the idle rule applies as usual.
func TestConsumeStreamDeltas_IdleAfterContent(t *testing.T) {
	full := toolUseSSEResponse("msg_1", "toolu_1", "propose_changes", emptyAnswer("Done."), 10, 5)
	start := messageStartOnly()
	tests := []struct {
		name    string
		serve   func(w http.ResponseWriter, r *http.Request)
		wantErr error
	}{
		{
			name: "prefill silence after message_start is waited for",
			serve: func(w http.ResponseWriter, r *http.Request) {
				flush(w, start)
				select {
				case <-time.After(200 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				flush(w, strings.TrimPrefix(full, start))
			},
		},
		{
			name: "message_start then nothing fails on the first-token budget",
			serve: func(w http.ResponseWriter, r *http.Request) {
				flush(w, start)
				<-r.Context().Done()
			},
			wantErr: errStreamFirstToken,
		},
		{
			name: "silence after real output is still a stall",
			serve: func(w http.ResponseWriter, r *http.Request) {
				var b strings.Builder
				sseEvent(&b, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
				sseEvent(&b, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Working"}})
				flush(w, start+b.String())
				<-r.Context().Done()
			},
			wantErr: errStreamIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				tt.serve(w, r)
			}))
			defer ts.Close()
			stream := newStreamingTestStream(ts)
			var message anthropic.Message
			err := consumeStreamDeltas(context.Background(), stream, &message, 50*time.Millisecond, 400*time.Millisecond, true, nil, nil, nil)
			_ = stream.Close()
			if !errors.Is(err, tt.wantErr) && (tt.wantErr != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// A host that writes a tool call whole sends only ping events meanwhile, which the SDK drops; the bytes still prove
// the connection is alive, so the call must complete on its first attempt. The same silence without pings is a stall.
func TestGenerate_PingsKeepAToolCallInProgressAlive(t *testing.T) {
	var start strings.Builder
	sseEvent(&start, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	sseEvent(&start, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	sseEvent(&start, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Writing it now."}})
	sseEvent(&start, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	full := toolUseSSEResponse("msg_1", "toolu_1", toolNameProposeChanges, emptyAnswer("done"), 10, 5)
	_, rest, ok := strings.Cut(full, "event: content_block_start")
	if !ok {
		t.Fatal("fixture has no content_block_start")
	}
	toolPart := strings.ReplaceAll("event: content_block_start"+rest, `"index":0`, `"index":1`)

	for _, pings := range []bool{true, false} {
		t.Run(fmt.Sprintf("pings=%v", pings), func(t *testing.T) {
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				flush(w, start.String())
				for i := 0; i < 10; i++ {
					select {
					case <-time.After(30 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
					if pings {
						flush(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
					}
				}
				flush(w, toolPart)
			}))
			defer ts.Close()
			g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
			g.streamTimeouts.Idle = 100 * time.Millisecond
			g.streamTimeouts.FirstTokenEdit = 2 * time.Second

			result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "hello", nil, nil, nil, nil)
			if pings {
				if err != nil || calls != 1 || result.Summary != "done" {
					t.Fatalf("want one attempt that completes, got calls=%d err=%v", calls, err)
				}
				return
			}
			if calls < 2 {
				t.Fatalf("want 300ms of true silence after output treated as a stall and retried, got %d call(s), err=%v", calls, err)
			}
		})
	}
}
