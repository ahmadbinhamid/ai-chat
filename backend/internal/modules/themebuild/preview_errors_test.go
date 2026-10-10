package themebuild

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/previewerrors"
)

const previewErrorMarker = "PREVIEW-ERROR-MARKER"

var samplePreviewErrors = []previewerrors.Entry{
	{Type: "error", Message: previewErrorMarker + " Cannot read properties of null", Source: "js/minicart.js", Line: 42, Column: 17, Count: 1},
}

func TestPromptWithAttachments(t *testing.T) {
	plain := GenerateInput{}
	if got, want := promptWithAttachments("fix the cart", plain), "fix the cart\n\n"+proposeInstruction; got != want {
		t.Errorf("a message with nothing attached should be the request plus the tool instruction, got %q", got)
	}

	withErrors := GenerateInput{PreviewErrors: samplePreviewErrors}
	got := promptWithAttachments("fix the cart", withErrors)
	if !strings.HasPrefix(got, "--- Browser errors captured from the preview") || !strings.HasSuffix(got, "fix the cart\n\n"+proposeInstruction) {
		t.Errorf("expected the errors block first and the request last, got %q", got)
	}
	if !strings.Contains(got, "Uncaught error — js/minicart.js line 42 (×1): "+previewErrorMarker) {
		t.Errorf("expected the formatted error line, got %q", got)
	}
}

// systemText returns the concatenated system blocks of a recorded model request.
func systemText(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	var b strings.Builder
	for _, s := range req.System {
		b.WriteString(s.Text)
	}
	return b.String()
}

// A repair round that resumes the conversation must not append the block again, and it never reaches the system prompt.
func TestCheckAndRepair_ResumeDoesNotRepeatPreviewErrors(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_read", "read_theme_file", map[string]any{"paths": []string{"pages/offers.liquid"}}}),
		sseToolTurn("msg_2", [3]any{"toolu_propose", "propose_changes", offersProposal(badPageContent)}),
		sseToolTurn("msg_3", [3]any{"toolu_fix", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	gen := realGenerator(t, ts.URL)
	svc := &Service{gen: gen}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", PreviewErrors: samplePreviewErrors}
	tc := ai.ThemeContext{ThemeSlug: "demo"}

	first, err := gen.Generate(context.Background(), tc, nil, promptWithAttachments("fix the cart", in), nil, nil, readOutputExec, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if _, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", "gen-1", tc, nil, first, testSnapshot(), readOutputExec, nil, nil); err != nil {
		t.Fatalf("checkAndRepair: %v", err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("expected one repair call, got %d calls", len(*bodies))
	}
	if n := strings.Count((*bodies)[2], previewErrorMarker); n != 1 {
		t.Errorf("expected the preview errors exactly once in the resumed repair request, got %d", n)
	}
	for i, body := range *bodies {
		if strings.Contains(systemText(t, body), previewErrorMarker) {
			t.Errorf("request %d: preview errors leaked into the system prompt", i)
		}
	}
}

func TestGenerateValidProposal_RetryDoesNotRepeatPreviewErrors(t *testing.T) {
	bodies, ts := scriptedModel(t,
		sseToolTurn("msg_1", [3]any{"toolu_read", "read_theme_file", map[string]any{"paths": []string{"pages/offers.liquid"}}}),
		sseToolTurn("msg_2", [3]any{"toolu_svg", "propose_changes", svgProposal()}),
		sseToolTurn("msg_3", [3]any{"toolu_good", "propose_changes", offersProposal(goodPageContent)}),
	)
	defer ts.Close()
	svc := &Service{gen: realGenerator(t, ts.URL)}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", PreviewErrors: samplePreviewErrors}

	if _, _, err := svc.generateValidProposal(context.Background(), &ai.ThemeContext{ThemeSlug: "demo"}, nil, "fix the cart", readOutputExec, nil, nil, in); err != nil {
		t.Fatalf("generateValidProposal: %v", err)
	}
	if n := strings.Count((*bodies)[2], previewErrorMarker); n != 1 {
		t.Errorf("expected the preview errors exactly once in the retry request, got %d", n)
	}
}

// historyCapturingGenerator records every call's prompt and history.
type historyCapturingGenerator struct {
	mu        sync.Mutex
	prompts   []string
	histories [][]ai.Turn
}

func (g *historyCapturingGenerator) Generate(_ context.Context, _ ai.ThemeContext, history []ai.Turn, prompt string, _ []ai.Image, _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prompts = append(g.prompts, prompt)
	g.histories = append(g.histories, append([]ai.Turn(nil), history...))
	return &ai.Result{Summary: "ok", AnsweredQuestion: true}, nil
}

func (g *historyCapturingGenerator) SupportsVision() bool { return false }

func (g *historyCapturingGenerator) Summarize(context.Context, []ai.Turn) (string, error) {
	return "", nil
}

// Stored as a console attachment, shown to the model for its own turn, and absent from every later turn's prompt and history.
func TestDoGenerate_PreviewErrorsThisTurnOnly(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &historyCapturingGenerator{}
	svc.gen = gen
	tenantID := uint64(time.Now().UnixNano())

	withNoise := append(append([]previewerrors.Entry{}, samplePreviewErrors...),
		previewerrors.Entry{Type: "resource", Message: "failed to load", Source: "https://evil.example/x.js", Count: 1})
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "the add to cart button does nothing", PreviewErrors: withNoise,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	stored, err := chatSvc.GetAttachmentsContent(context.Background(), outcome.UserMessage.ID)
	if err != nil {
		t.Fatalf("GetAttachmentsContent: %v", err)
	}
	if len(stored) != 1 || stored[0].Kind != chat.AttachmentKindConsole || stored[0].MediaType != "application/json" {
		t.Fatalf("expected one console attachment, got %+v", stored)
	}
	entries, err := previewerrors.Parse(stored[0].Content)
	if err != nil || len(entries) != 2 || entries[1].Source != "" {
		t.Fatalf("expected both errors stored with the URL source dropped, got %+v (%v)", entries, err)
	}

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range messages {
		if strings.Contains(m.Content, previewErrorMarker) {
			t.Errorf("preview errors leaked into merchant-facing content: %q", m.Content)
		}
		if m.ID == outcome.UserMessage.ID && (len(m.Attachments) != 1 || m.Attachments[0].Kind != chat.AttachmentKindConsole) {
			t.Errorf("expected the transcript metadata to show the console attachment, got %+v", m.Attachments)
		}
	}

	if _, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "still broken",
	}); err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	gen.mu.Lock()
	defer gen.mu.Unlock()
	if len(gen.prompts) != 2 {
		t.Fatalf("expected 2 Generate calls, got %d", len(gen.prompts))
	}
	if !strings.Contains(gen.prompts[0], previewErrorMarker) {
		t.Errorf("expected turn 1's prompt to carry its preview errors, got %q", gen.prompts[0])
	}
	if strings.Contains(gen.prompts[1], previewErrorMarker) {
		t.Errorf("preview errors were carried forward into turn 2's prompt: %q", gen.prompts[1])
	}
	for _, turn := range gen.histories[1] {
		if strings.Contains(turn.Content, previewErrorMarker) {
			t.Errorf("preview errors leaked into turn 2's history: %q", turn.Content)
		}
	}
}

func TestPromptWithAttachments_SandboxErrorNote(t *testing.T) {
	turn2 := promptWithAttachments("Invalid base URL", GenerateInput{Prompt: "Invalid base URL"})
	if !strings.HasSuffix(turn2, "Invalid base URL\n\n"+proposeInstruction) || !strings.Contains(turn2, previewerrors.SandboxErrorNote) {
		t.Errorf("expected the sandbox note before the exact turn-2 message, got %q", turn2)
	}
	plain := promptWithAttachments("add to cart does nothing", GenerateInput{Prompt: "add to cart does nothing"})
	if strings.Contains(plain, previewerrors.SandboxErrorNote) {
		t.Errorf("a message without a sandbox error must get no sandbox note, got %q", plain)
	}
	// Repairs pass their own text but the merchant's message is what's checked, so the flat fallback keeps the note.
	repair := promptWithAttachments("Your last proposal failed validation…", GenerateInput{Prompt: "Invalid base URL"})
	if !strings.Contains(repair, previewerrors.SandboxErrorNote) {
		t.Error("expected the note on a flat-fallback repair for the same turn")
	}
}
