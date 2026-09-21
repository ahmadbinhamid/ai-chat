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

// newStreamingTestStream opens a real *ssestream.Stream against ts by
// issuing a minimal Messages.NewStreaming call — used by the consumeStream
// tests below, which need a real stream (consumeStream takes the SDK's
// stream type directly, not an interface) rather than Generate's full tool
// loop.
func newStreamingTestStream(ts *httptest.Server) *ssestream.Stream[anthropic.MessageStreamEventUnion] {
	client := anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("test-key"))
	return client.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
		Model:     "claude-test",
		MaxTokens: 1024,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
}

// flush writes s to w and flushes it immediately, so a test server can send
// a partial SSE response and then stall — http.ResponseWriter otherwise may
// buffer until the handler returns, which would defeat every test below
// that depends on the client seeing bytes before the handler blocks.
func flush(w http.ResponseWriter, s string) {
	fmt.Fprint(w, s)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestConsumeStream_IdleTimeoutFiresOnStalledConnection covers
// streamIdleTimeout's whole reason to exist: a connection that goes quiet
// after producing at least one event must be treated as hung and returned
// as errStreamIdle, not left to block forever.
func TestConsumeStream_IdleTimeoutFiresOnStalledConnection(t *testing.T) {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})
	firstEvent := b.String()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, firstEvent)
		// Deliberately never send anything else — the exact "stalled but
		// still connected" scenario streamIdleTimeout exists to catch.
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message
	coalescer := newDeltaCoalescer(func(string) {})

	start := time.Now()
	err := consumeStream(context.Background(), stream, &message, coalescer, 50*time.Millisecond, time.Second)
	elapsed := time.Since(start)
	_ = stream.Close()

	if !errors.Is(err, errStreamIdle) {
		t.Fatalf("expected errStreamIdle, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("idle timeout took %v, expected roughly the configured 50ms", elapsed)
	}
}

// TestConsumeStream_FirstTokenTimeoutFiresWhenNothingEverArrives covers the
// other budget: a connection that never produces so much as one byte of
// real content must fail on firstTokenTimeout, independent of (and here,
// shorter than) the idle timeout.
func TestConsumeStream_FirstTokenTimeoutFiresWhenNothingEverArrives(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(w, "")
		<-r.Context().Done()
	}))
	defer ts.Close()

	stream := newStreamingTestStream(ts)
	var message anthropic.Message
	coalescer := newDeltaCoalescer(func(string) {})

	err := consumeStream(context.Background(), stream, &message, coalescer, time.Second, 50*time.Millisecond)
	_ = stream.Close()

	if !errors.Is(err, errStreamFirstToken) {
		t.Fatalf("expected errStreamFirstToken, got %v", err)
	}
}

// TestConsumeStream_ToolUseBytesCountAsFirstTokenProgress is the load-
// bearing regression case: a forced propose_changes call (or any call with
// adaptive thinking off) can stream nothing but tool_use deltas with zero
// narration text. If only text/thinking counted as progress, a model that
// was actively streaming a large tool_use payload would still trip the
// first-token timeout. Here the server sends only a tool_use content block
// (no text) and then stalls — the first-token timeout must NOT be what
// fires; only the (longer) idle timeout should, proving the tool_use bytes
// were recognized as progress and stopped the first-token timer.
func TestConsumeStream_ToolUseBytesCountAsFirstTokenProgress(t *testing.T) {
	var b strings.Builder
	sseEvent(&b, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
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
	coalescer := newDeltaCoalescer(func(string) {})

	// firstToken shorter than idle: if tool_use bytes did NOT count as
	// progress, this would return errStreamFirstToken at ~40ms instead.
	err := consumeStream(context.Background(), stream, &message, coalescer, 150*time.Millisecond, 40*time.Millisecond)
	_ = stream.Close()

	if !errors.Is(err, errStreamIdle) {
		t.Fatalf("expected the idle timeout to fire (proving tool_use content counted as first-token progress), got %v", err)
	}
}

// TestGenerate_RetriesOnIdleTimeout is the integration case: a stalled
// first attempt must be retried exactly like a truncated/garbled stream
// chunk already was, ending in a successful Result once a later attempt
// completes normally — not a hard-failed generation.
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

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "hello", nil, nil, nil, nil, nil)
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

// TestStreamProgressBytes_CountsTextThinkingAndToolUse covers every branch
// streamProgressBytes is supposed to count — see its own doc comment on why
// tool_use must count alongside text/thinking.
//
// Content blocks are built via real json.Unmarshal, not a struct literal:
// ContentBlockUnion's As* accessors (AsText/AsThinking/AsToolUse, which
// AsAny/streamProgressBytes go through) re-decode from an internal raw-JSON
// field that only json.Unmarshal populates — a struct literal leaves it
// empty and every accessor silently returns a zero value.
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

// TestStreamProgressBytes_EmptyMessageIsZero guards the "no progress yet"
// baseline consumeStream's sawProgress check relies on.
func TestStreamProgressBytes_EmptyMessageIsZero(t *testing.T) {
	if got := streamProgressBytes(anthropic.Message{}); got != 0 {
		t.Fatalf("expected 0 for an empty message, got %d", got)
	}
}

// TestClampToContextDeadline_ShortensWhenParentDeadlineIsSooner covers the
// reason clampToContextDeadline exists: a first-token budget must never
// itself outlive the generation it's part of.
func TestClampToContextDeadline_ShortensWhenParentDeadlineIsSooner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	got := clampToContextDeadline(ctx, time.Hour)
	if got <= 0 || got > 20*time.Millisecond {
		t.Fatalf("expected a duration clamped to roughly the 20ms remaining on ctx, got %v", got)
	}
}

// TestClampToContextDeadline_LeavesUnchangedWithNoDeadline covers the
// common case in tests (context.Background()) and any caller without a
// parent deadline — clamping must be a no-op, not a zero timeout.
func TestClampToContextDeadline_LeavesUnchangedWithNoDeadline(t *testing.T) {
	got := clampToContextDeadline(context.Background(), 45*time.Second)
	if got != 45*time.Second {
		t.Fatalf("expected d unchanged with no ctx deadline, got %v", got)
	}
}

// TestClampToContextDeadline_NeverGoesNegative covers a ctx whose deadline
// has already passed — the caller must get 0, not a negative duration that
// would make a subsequent time.NewTimer panic.
func TestClampToContextDeadline_NeverGoesNegative(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if got := clampToContextDeadline(ctx, time.Minute); got != 0 {
		t.Fatalf("expected 0 for an already-expired deadline, got %v", got)
	}
}

// TestIsRetryableStreamErr_ExcludesContextCancellation covers a deliberate
// exclusion: ctx.Err() (the generation's own parent budget running out, or
// an explicit cancel) must never be treated as a retryable provider hiccup
// — retrying it would just spend the retry delay against a context that's
// already dead.
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

// TestGenerator_FirstTokenTimeoutFor_MapsGenerationModes covers
// StreamTimeouts' per-mode mapping and its zero-value fallback to the
// package defaults.
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

// TestGenerator_FirstTokenTimeoutFor_DefaultsWhenZero covers a Generator
// built without New (e.g. a bare struct literal in a test) — a zero
// StreamTimeouts must fall back to the package defaults, never to an
// instant/zero timeout.
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
