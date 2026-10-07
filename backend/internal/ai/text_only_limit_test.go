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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func emptyAnswer(summary string) map[string]any {
	return map[string]any{
		"summary": summary, "needs_clarification": false, "answered_question": true, "files": []any{},
		"page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
	}
}

// scriptedSSE serves turns in order and fails the test on any extra call.
func scriptedSSE(t *testing.T, turns ...string) (*int, *httptest.Server) {
	t.Helper()
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > len(turns) {
			t.Errorf("unexpected model call %d", calls)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, turns[calls-1])
	}))
	return &calls, ts
}

func TestGenerate_StopsAfterThreeConsecutiveTextRounds(t *testing.T) {
	fakeSuccess := "Done! I've redesigned your homepage."
	calls, ts := scriptedSSE(t,
		textOnlySSEResponse("msg_1", fakeSuccess, 10, 5),
		textOnlySSEResponse("msg_2", fakeSuccess, 10, 5),
		textOnlySSEResponse("msg_3", fakeSuccess, 10, 5),
	)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "redesign home", nil, nil, nil, nil)
	if !errors.Is(err, errStuckInTextReplies) {
		t.Fatalf("expected errStuckInTextReplies, got %v", err)
	}
	if *calls != maxConsecutiveTextOnlyRounds {
		t.Errorf("expected exactly %d model calls, got %d", maxConsecutiveTextOnlyRounds, *calls)
	}
	if result != nil {
		t.Errorf("the model's text must never be returned as an answer, got %+v", result)
	}
}

func TestGenerate_RealToolCallResetsTextCount(t *testing.T) {
	calls, ts := scriptedSSE(t,
		textOnlySSEResponse("msg_1", "Let me look.", 10, 5),
		textOnlySSEResponse("msg_2", "Looking now.", 10, 5),
		toolUseSSEResponse("msg_3", "toolu_3", "list_theme_files", map[string]any{}, 10, 5),
		textOnlySSEResponse("msg_4", "Found it.", 10, 5),
		textOnlySSEResponse("msg_5", "Writing it up.", 10, 5),
		toolUseSSEResponse("msg_6", "toolu_6", "propose_changes", emptyAnswer("Here's what I found."), 10, 5),
	)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "pages/home.liquid", nil }

	result, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "demo"}, nil, "what's on my home page?", nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("expected two text rounds either side of a real call to carry on, got %v", err)
	}
	if *calls != 6 || result.Summary != "Here's what I found." {
		t.Errorf("unexpected outcome: %d calls, result %+v", *calls, result)
	}
}

func TestSanitizeError_StuckInTextIsHonest(t *testing.T) {
	msg := SanitizeError(fmt.Errorf("retry generation: %w", errStuckInTextReplies))
	if strings.Contains(msg, "too complex") || !strings.Contains(msg, "rephrase") {
		t.Errorf("expected the honest rephrase message, got %q", msg)
	}
	exhausted := SanitizeError(fmt.Errorf("model did not call propose_changes within %d tool-loop iterations", maxToolIterations))
	if !strings.Contains(exhausted, ExhaustedSearchReply) {
		t.Errorf("expected the honest couldn't-find-the-cause message for the 28-round exhaustion, got %q", exhausted)
	}
	if exhausted == msg {
		t.Error("the stuck-in-text and 28-round exhaustion messages must differ")
	}
}
