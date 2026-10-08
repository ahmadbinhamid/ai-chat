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

// scriptedModel serves turns in order and records each request body.
func scriptedModel(t *testing.T, turns ...string) (*[]string, *httptest.Server) {
	t.Helper()
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) > len(turns) {
			t.Errorf("unexpected model call %d", len(bodies))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, turns[len(bodies)-1])
	}))
	return &bodies, ts
}

// assertToolPairing checks every assistant tool_use is answered, in order, by the tool_result blocks
// leading the very next user message, and no tool_result appears anywhere else.
func assertToolPairing(t *testing.T, body string) {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				ToolUseID string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	var pending []string
	for i, m := range req.Messages {
		if m.Role == "assistant" {
			if len(pending) > 0 {
				t.Fatalf("message %d: assistant turn while %v unanswered", i, pending)
			}
			pending = nil
			for _, b := range m.Content {
				if b.Type == "tool_use" {
					pending = append(pending, b.ID)
				}
			}
			continue
		}
		var results []string
		for j, b := range m.Content {
			if b.Type != "tool_result" {
				continue
			}
			if j != len(results) {
				t.Fatalf("message %d: tool_result after a non-tool_result block", i)
			}
			results = append(results, b.ToolUseID)
		}
		if strings.Join(results, ",") != strings.Join(pending, ",") {
			t.Fatalf("message %d: tool_results %v don't match tool_uses %v", i, results, pending)
		}
		pending = nil
	}
	if len(pending) > 0 {
		t.Fatalf("request ends with unanswered tool_uses %v", pending)
	}
}

func svgProposal() map[string]any {
	p := offersProposal(goodPageContent)
	p["files"] = []any{map[string]any{"path": "images/coffee-hero.svg", "action": "create", "content": "<svg/>", "edits": []any{}}}
	return p
}

func emptyProposal() map[string]any {
	p := offersProposal(goodPageContent)
	p["files"] = []any{}
	return p
}

func realGenerator(t *testing.T, url string) *ai.Generator {
	t.Helper()
	gen := newSingleModelGenerator(t, url, "test-model", "medium", "")
	return gen
}

func readOutputExec(context.Context, string, json.RawMessage) (string, error) {
	return "READ-OUTPUT", nil
}

// An invalid proposal (the observed .svg case) resumes the tool loop: prior reads stay, the rejected
// proposal is answered with its recap, and the correction is the next user turn.
func TestGenerateValidProposal_InvalidProposalResumesToolLoop(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_read", "read_theme_file", map[string]any{"paths": []string{"pages/offers.liquid"}}}),
		sseToolTurn("msg_2", [3]any{"toolu_svg", "propose_changes", svgProposal()}),
		sseToolTurn("msg_3", [3]any{"toolu_good", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	svc := &Service{gen: realGenerator(t, ts.URL)}
	filename, attachment := "ref.html", "<p>ATTACHMENT-BODY</p>"
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &attachment}

	result, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{ThemeSlug: "demo"}, nil, "redesign", readOutputExec, nil, nil, in)
	if err != nil {
		t.Fatalf("generateValidProposal: %v", err)
	}
	if len(*bodies) != 3 || result.Files[0].Path != "pages/offers.liquid" {
		t.Fatalf("expected one retry producing the valid proposal, got %d calls", len(*bodies))
	}

	retry := (*bodies)[2]
	assertToolPairing(t, retry)
	for _, want := range []string{`"id":"toolu_read"`, "READ-OUTPUT", `"tool_use_id":"toolu_svg"`, "### images/coffee-hero.svg (create)", "That reply wasn't valid", `\".svg\" is not allowed`} {
		if !strings.Contains(retry, want) {
			t.Errorf("expected retry request to contain %s", want)
		}
	}
	if n := strings.Count(retry, "ATTACHMENT-BODY"); n != 1 {
		t.Errorf("expected the attachment exactly once, got %d", n)
	}
}

// An empty, unexplored proposal still has a propose_changes tool_use, so its retry resumes too.
func TestGenerateValidProposal_EmptyProposalResumes(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_empty", "propose_changes", emptyProposal()}),
		sseToolTurn("msg_2", [3]any{"toolu_read", "read_theme_file", map[string]any{"paths": []string{"pages/offers.liquid"}}}),
		sseToolTurn("msg_3", [3]any{"toolu_good", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	svc := &Service{gen: realGenerator(t, ts.URL)}

	if _, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{ThemeSlug: "demo"}, nil, "redesign", readOutputExec, nil, nil, GenerateInput{TenantID: 1, ThemeSlug: "demo"}); err != nil {
		t.Fatalf("generateValidProposal: %v", err)
	}
	retry := (*bodies)[1]
	assertToolPairing(t, retry)
	for _, want := range []string{`"tool_use_id":"toolu_empty"`, "proposed an empty files array"} {
		if !strings.Contains(retry, want) {
			t.Errorf("expected retry request to contain %s", want)
		}
	}
}

// Invalid, then empty, then valid: the whole shared budget chains one conversation with valid pairing.
func TestGenerateValidProposal_SharedBudgetKeepsPairing(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_svg", "propose_changes", svgProposal()}),
		sseToolTurn("msg_2", [3]any{"toolu_empty", "propose_changes", emptyProposal()}),
		sseToolTurn("msg_3", [3]any{"toolu_good", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	svc := &Service{gen: realGenerator(t, ts.URL)}

	if _, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{ThemeSlug: "demo"}, nil, "redesign", readOutputExec, nil, nil, GenerateInput{TenantID: 1, ThemeSlug: "demo"}); err != nil {
		t.Fatalf("generateValidProposal: %v", err)
	}
	if len(*bodies) != maxThemeCheckRetries+1 {
		t.Fatalf("expected %d calls, got %d", maxThemeCheckRetries+1, len(*bodies))
	}
	for i, body := range (*bodies)[1:] {
		assertToolPairing(t, body)
		if i == 1 {
			for _, want := range []string{`"tool_use_id":"toolu_svg"`, `"tool_use_id":"toolu_empty"`, "That reply wasn't valid", "proposed an empty files array"} {
				if !strings.Contains(body, want) {
					t.Errorf("final retry missing %s", want)
				}
			}
		}
	}
}

// Fake mode produces no conversation, so retries take the flat path and still reach the honest fallback.
func TestGenerateValidProposal_FakeModeUnaffected(t *testing.T) {
	svc := &Service{gen: ai.NewFake(0)}
	result, _, err := svc.generateValidProposal(context.Background(), ai.ThemeContext{}, nil, "hi", nil, nil, nil, GenerateInput{})
	if err != nil {
		t.Fatalf("generateValidProposal: %v", err)
	}
	if result.Conversation() != nil {
		t.Error("expected no conversation in fake mode")
	}
	if result.Summary != emptyProposalFallbackSummary {
		t.Errorf("expected the empty-proposal fallback after exhausting retries, got %q", result.Summary)
	}
}
