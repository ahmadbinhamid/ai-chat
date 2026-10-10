package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

// sseToolTurn renders one streamed assistant turn calling each tool in order ({id, name, input}).
func sseToolTurn(msgID string, calls ...[3]any) string {
	var b strings.Builder
	write := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, encoded)
	}
	write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": msgID, "type": "message", "role": "assistant", "model": "m", "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
	}})
	for i, c := range calls {
		input, _ := json.Marshal(c[2])
		write("content_block_start", map[string]any{"type": "content_block_start", "index": i,
			"content_block": map[string]any{"type": "tool_use", "id": c[0], "name": c[1], "input": map[string]any{}}})
		write("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	write("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 5}})
	write("message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}

func offersProposal(content string) map[string]any {
	return map[string]any{
		"summary": "offers page", "needs_clarification": false, "answered_question": false,
		"files":               []any{map[string]any{"path": "pages/offers.liquid", "action": "update", "content": content, "edits": []any{}}},
		"page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
	}
}

// A themecheck rejection resumes the first tool loop instead of rebuilding from flat turns.
func TestCheckAndRepair_ResumesToolLoopConversation(t *testing.T) {
	turns := []string{
		sseToolTurn("msg_1", [3]any{"toolu_read", "read_theme_file", map[string]any{"paths": []string{"pages/offers.liquid"}}}),
		sseToolTurn("msg_2", [3]any{"toolu_propose", "propose_changes", offersProposal(badPageContent)}),
		sseToolTurn("msg_3", [3]any{"toolu_fix", "propose_changes", offersProposal(goodPageContent)}),
	}
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, turns[len(bodies)-1])
	}))
	defer ts.Close()

	gen := newSingleModelGenerator(t, ts.URL, "test-model", "medium", "")
	svc := &Service{gen: gen}
	filename, attachment := "ref.html", "<p>ATTACHMENT-BODY</p>"
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &attachment}
	tc := ai.ThemeContext{ThemeSlug: "demo"}
	toolExec := func(context.Context, string, json.RawMessage) (string, error) { return "READ-OUTPUT", nil }

	first, err := gen.Generate(context.Background(), tc, nil, promptWithAttachments("build offers", in), nil, nil, toolExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", "gen-1", tc, nil, first, testSnapshot(), toolExec, nil, nil)
	if err != nil {
		t.Fatalf("checkAndRepair: %v", err)
	}
	if len(bodies) != 3 || got.Files[0].Content != goodPageContent {
		t.Fatalf("expected one repair call producing the good page, got %d calls", len(bodies))
	}

	repair := bodies[2]
	for _, want := range []string{`"id":"toolu_read"`, `"tool_use_id":"toolu_read"`, "READ-OUTPUT", `"tool_use_id":"toolu_propose"`, "### pages/offers.liquid (update)"} {
		if !strings.Contains(repair, want) {
			t.Errorf("expected repair request to contain %s", want)
		}
	}
	if n := strings.Count(repair, "ATTACHMENT-BODY"); n != 1 {
		t.Errorf("expected the HTML attachment exactly once in the resumed conversation, got %d", n)
	}
	if !strings.Contains(repair, "Your last proposal failed validation") {
		t.Error("expected the repair prompt in the resumed request")
	}
}
