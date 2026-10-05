package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

// sseTextTurn renders one model turn that replies with plain text only (no tool_use block).
func sseTextTurn(msgID, text string) string {
	var b strings.Builder
	write := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, encoded)
	}
	write("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": msgID, "type": "message", "role": "assistant", "model": "m", "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
	}})
	write("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	write("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 5}})
	write("message_stop", map[string]any{"type": "message_stop"})
	return b.String()
}

func textCall(args map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{"name": "propose_changes", "arguments": args})
	return "```json\n" + string(encoded) + "\n```"
}

// A recovered "Done!" with no files and no exploration is still the fake success isUnexploredEmptyProposal exists to reject.
func TestRecoveredEmptyUnexploredProposalStillRejected(t *testing.T) {
	fake := emptyProposal()
	fake["summary"] = "Done! I've redesigned your homepage."
	_, ts := scriptedModel(t, sseTextTurn("msg_1", textCall(fake)))
	defer ts.Close()

	result, err := realGenerator(t, ts.URL).Generate(context.Background(), ai.ThemeContext{ThemeSlug: "demo"}, nil, "redesign home", nil, nil, readOutputExec, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Summary != "Done! I've redesigned your homepage." {
		t.Fatalf("expected the text call to be recovered, got %+v", result)
	}
	if !isUnexploredEmptyProposal(result) {
		t.Error("a recovered empty, unexplored proposal must still be rejected by isUnexploredEmptyProposal")
	}
}

// A recovered proposal has no conversation, so a themecheck repair uses the flat-recap fallback.
func TestCheckAndRepair_RecoveredProposalUsesFlatFallback(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseTextTurn("msg_1", textCall(offersProposal(badPageContent))),
		sseToolTurn("msg_2", [3]any{"toolu_fix", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	gen := realGenerator(t, ts.URL)
	svc := &Service{gen: gen}
	tc := ai.ThemeContext{ThemeSlug: "demo"}

	first, err := gen.Generate(context.Background(), tc, nil, "build offers", nil, nil, readOutputExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if first.Conversation() != nil {
		t.Fatal("expected a recovered proposal to carry no conversation")
	}
	got, _, err := svc.checkAndRepair(context.Background(), GenerateInput{TenantID: 1, ThemeSlug: "demo"}, "chat-1", tc, nil, first, testSnapshot(), readOutputExec, nil, nil)
	if err != nil {
		t.Fatalf("checkAndRepair: %v", err)
	}
	if len(*bodies) != 2 || got.Files[0].Content != goodPageContent {
		t.Fatalf("expected one repair call producing the good page, got %d calls", len(*bodies))
	}
	repair := (*bodies)[1]
	if strings.Contains(repair, "tool_use_id") {
		t.Error("the flat fallback must not pair any tool_result")
	}
	for _, want := range []string{"### pages/offers.liquid (update)", "Your last proposal failed validation"} {
		if !strings.Contains(repair, want) {
			t.Errorf("expected the flat repair request to contain %q", want)
		}
	}
}

// A repair round stuck in text fails fast with the honest error, not after the full tool-loop budget.
func TestCheckAndRepair_RepairStuckInTextFailsFast(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_propose", "propose_changes", offersProposal(badPageContent)}),
		sseTextTurn("msg_2", "I'll fix that."), sseTextTurn("msg_3", "Fixing now."), sseTextTurn("msg_4", "Done!"),
	)
	defer ts.Close()
	gen := realGenerator(t, ts.URL)
	svc := &Service{gen: gen}
	tc := ai.ThemeContext{ThemeSlug: "demo"}

	first, err := gen.Generate(context.Background(), tc, nil, "build offers", nil, nil, readOutputExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	_, _, err = svc.checkAndRepair(context.Background(), GenerateInput{TenantID: 1, ThemeSlug: "demo"}, "chat-1", tc, nil, first, testSnapshot(), readOutputExec, nil, nil)
	if err == nil {
		t.Fatal("expected the stuck repair to fail")
	}
	if len(*bodies) != 4 {
		t.Errorf("expected the repair to stop after 3 text rounds (4 calls total), got %d", len(*bodies))
	}
	if msg := ai.SanitizeError(err); !strings.Contains(msg, "rephrase") {
		t.Errorf("expected the honest message, got %q", msg)
	}
}
