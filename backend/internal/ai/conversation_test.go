package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// scriptedServer answers model calls with turns in order and records each request body.
type scriptedServer struct {
	t      *testing.T
	mu     sync.Mutex
	turns  []string
	bodies [][]byte
}

func newScriptedServer(t *testing.T, turns ...string) (*scriptedServer, *httptest.Server) {
	s := &scriptedServer{t: t, turns: turns}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.bodies) >= len(s.turns) {
			s.t.Errorf("unexpected model call %d", len(s.bodies)+1)
			return
		}
		s.bodies = append(s.bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, s.turns[len(s.bodies)-1])
	}))
	return s, ts
}

type wireBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
}

func parseRequest(t *testing.T, body []byte) wireRequest {
	t.Helper()
	var req wireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

func toolResultText(b wireBlock) string {
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(b.Content, &parts)
	var s strings.Builder
	for _, p := range parts {
		s.WriteString(p.Text)
	}
	return s.String()
}

// assertValidPairing checks every assistant tool_use is answered, in order, by the tool_result blocks
// leading the very next user message, and no tool_result appears anywhere else.
func assertValidPairing(t *testing.T, msgs []wireMessage) {
	t.Helper()
	var pending []string
	for i, m := range msgs {
		var ids []string
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				ids = append(ids, b.ID)
			}
		}
		if m.Role == "assistant" {
			if len(pending) > 0 {
				t.Fatalf("message %d: assistant turn while tool_uses %v unanswered", i, pending)
			}
			pending = ids
			continue
		}
		var results []string
		leading := true
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				leading = false
				continue
			}
			if !leading {
				t.Fatalf("message %d: tool_result after a non-tool_result block", i)
			}
			results = append(results, b.ToolUseID)
		}
		if strings.Join(results, ",") != strings.Join(pending, ",") {
			t.Fatalf("message %d: tool_results %v don't match preceding tool_uses %v", i, results, pending)
		}
		pending = nil
	}
	if len(pending) > 0 {
		t.Fatalf("request ends with unanswered tool_uses %v", pending)
	}
}

func proposeTurn(id string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": toolNameProposeChanges, "input": map[string]any{
		"summary": "done", "needs_clarification": false, "answered_question": false,
		"files":               []any{map[string]any{"path": "pages/a.liquid", "action": "update", "content": "A", "edits": []any{}}},
		"page_registry_entry": nil, "layout_links_to_add": []string{}, "layout_scripts_to_add": []string{},
	}}
}

func readTurn(id string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": toolNameReadThemeFile, "input": map[string]any{"paths": []string{"pages/a.liquid"}}}
}

func echoToolExec(_ context.Context, name string, _ json.RawMessage) (string, error) {
	return "OUTPUT-OF-" + name, nil
}

// Golden captured from the pre-continuation code: a call without Continue must build exactly these messages.
func TestGenerate_FreshCallMessagesUnchanged(t *testing.T) {
	srv, ts := newScriptedServer(t, modelTurnSSE("msg_1", []map[string]any{proposeTurn("toolu_1")}))
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	g.visionModel = "vision-model"
	hist := []Turn{{Role: "user", Content: "make it blue"}, {Role: "assistant", Content: "Made it blue."}, {Role: "user", Content: "  "}}
	imgs := []Image{{Base64: "aGk=", MediaType: "image/png"}}

	if _, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "shop"}, hist, "now green", imgs, nil, nil, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var req struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(srv.bodies[0], &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	const golden = `[{"content":[{"text":"make it blue","type":"text"}],"role":"user"},{"content":[{"text":"Made it blue.","cache_control":{"ttl":"1h","type":"ephemeral"},"type":"text"}],"role":"assistant"},{"content":[{"source":{"data":"aGk=","media_type":"image/png","type":"base64"},"type":"image"},{"text":"now green","type":"text"}],"role":"user"}]`
	if string(req.Messages) != golden {
		t.Errorf("fresh-call messages changed:\n got %s\nwant %s", req.Messages, golden)
	}
	if req.Model != "vision-model" {
		t.Errorf("model = %q, want vision-model", req.Model)
	}
}

// A repair resumes the real tool loop: prior tool_use/tool_result blocks are sent, every tool_use in the
// accepted turn is answered (recap for the proposal, "not run" for a parallel read), images aren't duplicated.
func TestGenerate_ContinuationCarriesToolLoop(t *testing.T) {
	srv, ts := newScriptedServer(t,
		// Text-written tool call (no tool_use), which the loop nudges.
		modelTurnSSE("msg_1", []map[string]any{{"type": "text", "text": `{"name":"read_theme_file"}`}}),
		modelTurnSSE("msg_2", []map[string]any{{"type": "thinking", "thinking": "plan"}, readTurn("toolu_read")}),
		modelTurnSSE("msg_3", []map[string]any{readTurn("toolu_parallel"), proposeTurn("toolu_propose")}),
		modelTurnSSE("msg_4", []map[string]any{proposeTurn("toolu_repair")}),
	)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))
	g.visionModel = "vision-model"
	imgs := []Image{{Base64: "aGk=", MediaType: "image/png"}}
	tc := ThemeContext{ThemeSlug: "shop"}

	first, err := g.Generate(context.Background(), tc, []Turn{{Role: "user", Content: "earlier"}}, "redesign", imgs, nil, echoToolExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if first.Conversation() == nil {
		t.Fatal("expected an accepted proposal to carry a conversation")
	}

	tc.Continue = first.Conversation().WithProposalRecap("RECAP-CONTENT")
	if _, err := g.Generate(context.Background(), tc, []Turn{{Role: "user", Content: "IGNORED-HISTORY"}}, "REPAIR-PROMPT", imgs, nil, echoToolExec, nil); err != nil {
		t.Fatalf("repair Generate: %v", err)
	}

	repair := parseRequest(t, srv.bodies[3])
	assertValidPairing(t, repair.Messages)
	if repair.Model != "vision-model" {
		t.Errorf("repair model = %q, want the model the history was built with", repair.Model)
	}
	raw := string(srv.bodies[3])
	if strings.Contains(raw, "IGNORED-HISTORY") {
		t.Error("continued call must not rebuild from the history argument")
	}
	if n := strings.Count(raw, `"type":"image"`); n != 1 {
		t.Errorf("expected the image exactly once, got %d", n)
	}
	if !strings.Contains(raw, "OUTPUT-OF-read_theme_file") {
		t.Error("expected the first loop's tool_result output in the repair request")
	}

	last := repair.Messages[len(repair.Messages)-1]
	if last.Role != "user" || len(last.Content) != 3 {
		t.Fatalf("expected final user turn [tool_result, tool_result, text], got %+v", last)
	}
	if got := toolResultText(last.Content[0]); last.Content[0].ToolUseID != "toolu_parallel" || got != notRunToolResult {
		t.Errorf("parallel read result = %q/%q", last.Content[0].ToolUseID, got)
	}
	if got := toolResultText(last.Content[1]); last.Content[1].ToolUseID != "toolu_propose" || got != "RECAP-CONTENT" {
		t.Errorf("proposal result = %q/%q", last.Content[1].ToolUseID, got)
	}
	if last.Content[2].Type != "text" || last.Content[2].Text != "REPAIR-PROMPT" {
		t.Errorf("expected the repair prompt last, got %+v", last.Content[2])
	}
}

// Each round continues the previous one; pairing stays valid and growth is one loop per round.
func TestGenerate_ThreeRepairRoundsKeepPairing(t *testing.T) {
	srv, ts := newScriptedServer(t,
		modelTurnSSE("msg_1", []map[string]any{readTurn("toolu_r1")}),
		modelTurnSSE("msg_2", []map[string]any{proposeTurn("toolu_p1")}),
		modelTurnSSE("msg_3", []map[string]any{proposeTurn("toolu_p2")}),
		modelTurnSSE("msg_4", []map[string]any{readTurn("toolu_r3"), proposeTurn("toolu_p3")}),
		modelTurnSSE("msg_5", []map[string]any{proposeTurn("toolu_p4a"), proposeTurn("toolu_p4b")}),
	)
	defer ts.Close()
	g := newTestGenerator(anthropic.NewClient(option.WithBaseURL(ts.URL), option.WithAPIKey("k")))

	res, err := g.Generate(context.Background(), ThemeContext{ThemeSlug: "shop"}, nil, "redesign", nil, nil, echoToolExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	for round := 1; round <= 3; round++ {
		tc := ThemeContext{ThemeSlug: "shop", Continue: res.Conversation().WithProposalRecap(fmt.Sprintf("RECAP-%d", round))}
		if res, err = g.Generate(context.Background(), tc, nil, fmt.Sprintf("REPAIR-%d", round), nil, nil, echoToolExec, nil); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		body := srv.bodies[len(srv.bodies)-1]
		assertValidPairing(t, parseRequest(t, body).Messages)
		for prior := 1; prior < round; prior++ {
			if !strings.Contains(string(body), fmt.Sprintf("REPAIR-%d", prior)) {
				t.Errorf("round %d: expected earlier round's REPAIR-%d still in the conversation", round, prior)
			}
		}
	}
	// The final round's parallel propose_changes pair must still resolve: last one accepted, first superseded.
	msgs := res.Conversation().WithProposalRecap("R").resumeMessages("next")
	raw, _ := json.Marshal(msgs)
	var wire []wireMessage
	_ = json.Unmarshal(raw, &wire)
	assertValidPairing(t, wire)
	lastTurn := wire[len(wire)-1]
	if toolResultText(lastTurn.Content[0]) != supersededProposalResult || toolResultText(lastTurn.Content[1]) != "R" {
		t.Errorf("expected first proposal superseded and last accepted, got %+v", lastTurn.Content)
	}
}

func TestGenerate_FakeModeIgnoresContinuation(t *testing.T) {
	g := NewFake(0)
	tc := ThemeContext{Continue: (&Conversation{}).WithProposalRecap("x")}
	res, err := g.Generate(context.Background(), tc, nil, "hi", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(res.Summary, "[fake mode]") {
		t.Errorf("expected fake summary, got %q", res.Summary)
	}
	if res.Conversation() != nil {
		t.Error("expected no conversation from fake mode")
	}
}

func TestConversation_NilSafe(t *testing.T) {
	var c *Conversation
	if c.WithProposalRecap("x") != nil {
		t.Error("expected nil")
	}
	var r *Result
	if r.Conversation() != nil {
		t.Error("expected nil")
	}
}
