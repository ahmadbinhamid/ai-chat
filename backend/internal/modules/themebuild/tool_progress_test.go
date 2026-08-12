package themebuild

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestToolProgressEmitter_ToolStarted_EmitsToolCallWithPathOrPattern is
// item 3: each of the three theme tools' ToolStarted call must emit a
// tool_call event carrying enough detail to narrate — the path for
// read_theme_file, the pattern for grep_theme, nothing extra for
// list_theme_files (it has no interesting input to show).
func TestToolProgressEmitter_ToolStarted_EmitsToolCallWithPathOrPattern(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)
	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	tp := toolProgressFor(ctx, emitter)

	tp.ToolStarted("list_theme_files", json.RawMessage(`{}`))
	tp.ToolStarted("read_theme_file", json.RawMessage(`{"paths":["pages/home.liquid","other.liquid"]}`))
	tp.ToolStarted("grep_theme", json.RawMessage(`{"pattern":"hero","path_glob":"components/*.liquid"}`))

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 tool_call events, got %d: %+v", len(events), events)
	}

	for _, ev := range events {
		if ev.Type != EventTypeToolCall {
			t.Errorf("expected every event to be %q, got %q", EventTypeToolCall, ev.Type)
		}
	}

	var listPayload, readPayload, grepPayload struct {
		Tool    string `json:"tool"`
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(events[0].Payload, &listPayload); err != nil {
		t.Fatalf("failed to decode list_theme_files payload: %v", err)
	}
	if listPayload.Tool != "list_theme_files" || listPayload.Path != "" || listPayload.Pattern != "" {
		t.Errorf("expected a bare {tool} payload for list_theme_files, got %+v", listPayload)
	}

	if err := json.Unmarshal(events[1].Payload, &readPayload); err != nil {
		t.Fatalf("failed to decode read_theme_file payload: %v", err)
	}
	if readPayload.Tool != "read_theme_file" || readPayload.Path != "pages/home.liquid" {
		t.Errorf("expected read_theme_file's tool_call to carry its (first) path, got %+v", readPayload)
	}

	if err := json.Unmarshal(events[2].Payload, &grepPayload); err != nil {
		t.Fatalf("failed to decode grep_theme payload: %v", err)
	}
	if grepPayload.Tool != "grep_theme" || grepPayload.Pattern != "hero" {
		t.Errorf("expected grep_theme's tool_call to carry its pattern, got %+v", grepPayload)
	}
}

// TestToolProgressEmitter_ToolStarted_TruncatesLongPattern confirms the
// "truncate and sanitise the grep pattern" requirement: model-generated
// text landing in a payload the merchant's browser renders must be capped
// server-side, not trusted to be well-behaved.
func TestToolProgressEmitter_ToolStarted_TruncatesLongPattern(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)
	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	tp := toolProgressFor(ctx, emitter)

	longPattern := strings.Repeat("a", maxToolCallDetailChars*3)
	input, err := json.Marshal(map[string]string{"pattern": longPattern})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	tp.ToolStarted("grep_theme", input)

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var payload struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if len([]rune(payload.Pattern)) > maxToolCallDetailChars {
		t.Errorf("expected pattern truncated to at most %d runes, got %d", maxToolCallDetailChars, len([]rune(payload.Pattern)))
	}
}

// TestToolProgressEmitter_ToolFinished_EmitsEvenOnError is item 4:
// tool_result must always be emitted, including when the tool itself
// failed — the step list should show the failure, not a step that
// silently never resolves.
func TestToolProgressEmitter_ToolFinished_EmitsEvenOnError(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	ctx := context.Background()
	chatID := uuid.NewString()
	genID := seedGeneration(t, repo, chatID)
	emitter := newEventEmitter(ctx, repo, nil, genID, chatID)
	tp := toolProgressFor(ctx, emitter)

	tp.ToolFinished("read_theme_file", "3 lines", nil)
	tp.ToolFinished("grep_theme", "failed: boom", errors.New("boom"))

	events, err := repo.GetEventsSince(ctx, chatID, 0)
	if err != nil {
		t.Fatalf("GetEventsSince failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 tool_result events (success and failure both emit), got %d: %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.Type != EventTypeToolResult {
			t.Errorf("expected %q, got %q", EventTypeToolResult, ev.Type)
		}
	}

	var successPayload, failurePayload struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(events[0].Payload, &successPayload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if successPayload.Summary != "3 lines" {
		t.Errorf("expected summary %q, got %q", "3 lines", successPayload.Summary)
	}
	if err := json.Unmarshal(events[1].Payload, &failurePayload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failurePayload.Summary != "failed: boom" {
		t.Errorf("expected the error summary to be emitted, got %q", failurePayload.Summary)
	}
}
