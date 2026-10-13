package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/previewerrors"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

// promptRecordingGenerator returns results in order (last repeats) and records each prompt it was sent.
type promptRecordingGenerator struct {
	fakeGenerator
	prompts []string
}

func (g *promptRecordingGenerator) Generate(ctx context.Context, tc ai.ThemeContext, turns []ai.Turn, prompt string, images []ai.Image, p ai.ToolProgress, te ai.ToolExecutor, rf ai.FileReader) (*ai.Result, error) {
	g.prompts = append(g.prompts, prompt)
	return g.fakeGenerator.Generate(ctx, tc, turns, prompt, images, p, te, rf)
}

const (
	reversionPath  = "components/css/header.css"
	reversionSaved = ".header {\n  display: flex;\n  padding: 16px;\n}\n"
	// "Make the header background dark", still unapplied.
	reversionDraft = ".header {\n  display: flex;\n  padding: 16px;\n  background: var(--color-dark);\n" +
		"  color: var(--color-light);\n  border-bottom: 1px solid var(--color-border);\n}\n"
)

func reversionResult(content string) *ai.Result {
	return &ai.Result{Summary: "Fixed the add to cart button.", Files: []ai.GeneratedFile{
		{Path: reversionPath, Action: "update", Content: content},
	}}
}

func TestCheckAndRepair_DraftReversion(t *testing.T) {
	// Rebuilt from the saved file plus a cart tweak: the dark header is gone.
	reverted := strings.Replace(reversionSaved, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	kept := strings.Replace(reversionDraft, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	oneLineEdit := strings.Replace(reversionDraft, "padding: 16px;", "padding: 20px;", 1)

	tests := []struct {
		name        string
		prompt      string
		proposed    string
		wantRepair  bool
		wantWarning bool
	}{
		{name: "functionality fix that drops the earlier turn goes to repair", prompt: "the add to cart button does nothing, fix it",
			proposed: reverted, wantRepair: true},
		{name: "undo request passes without repair", prompt: "undo the header change", proposed: reverted},
		{name: "redesign request passes without repair", prompt: "redesign the header completely", proposed: reverted},
		{name: "one-line edit never trips the check", prompt: "make the header padding 20px", proposed: oneLineEdit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := &promptRecordingGenerator{fakeGenerator: fakeGenerator{results: []*ai.Result{reversionResult(kept)}}}
			svc := &Service{gen: gen, store: mapThemeStore{files: map[string]string{reversionPath: reversionSaved}}}
			in := GenerateInput{TenantID: 1, ThemeSlug: "demo", Prompt: tt.prompt, draft: map[string]string{reversionPath: reversionDraft}}

			got, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", "gen-1", ai.ThemeContext{}, nil, reversionResult(tt.proposed), testSnapshot(), nil, nil, nil)
			if err != nil {
				t.Fatalf("checkAndRepair: %v", err)
			}
			if !tt.wantRepair {
				if len(gen.prompts) != 0 {
					t.Fatalf("expected no repair round, got prompts %q", gen.prompts)
				}
				if got.Files[0].Content != tt.proposed {
					t.Error("a passing proposal must be returned unchanged")
				}
				return
			}
			if len(gen.prompts) != 1 {
				t.Fatalf("expected exactly one repair round, got %d", len(gen.prompts))
			}
			for _, want := range []string{
				"Your change to `" + reversionPath + "` removes earlier unsaved work the merchant hasn't asked to undo.",
				"background: var(--color-dark);",
				"border-bottom: 1px solid var(--color-border);",
			} {
				if !strings.Contains(gen.prompts[0], want) {
					t.Errorf("repair prompt missing %q:\n%s", want, gen.prompts[0])
				}
			}
			if got.Files[0].Content != kept {
				t.Errorf("expected the repaired proposal that keeps the dark header, got %q", got.Files[0].Content)
			}
		})
	}
}

// The undo case keeps today's warning: the post-repair check still flags it to the merchant.
func TestDraftReversionWarnings_StillWarnOnUndo(t *testing.T) {
	reverted := strings.Replace(reversionSaved, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	svc := &Service{store: mapThemeStore{files: map[string]string{reversionPath: reversionSaved}}}
	got := svc.draftReversionWarnings(context.Background(), themefs.RequestAuth{}, "chat-1",
		map[string]string{reversionPath: reversionDraft}, reversionResult(reverted))
	if len(got) != 1 || got[0].Severity != themecheck.SeverityWarning {
		t.Fatalf("expected one warning, got %+v", got)
	}
}

func TestPromptWithAttachments_FeatureNotes(t *testing.T) {
	tests := []struct {
		prompt                   string
		wantUntestable, wantCart bool
	}{
		{prompt: "the add to cart button does nothing, fix it", wantCart: true},
		{prompt: "the checkout button does nothing", wantUntestable: true},
		{prompt: "make the checkout button bigger"},
		{prompt: "restyle the cart page"},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			got := promptWithAttachments(tt.prompt, GenerateInput{Prompt: tt.prompt})
			if strings.Contains(got, previewerrors.UntestableFeatureNote) != tt.wantUntestable {
				t.Errorf("untestable note present = %v, want %v", !tt.wantUntestable, tt.wantUntestable)
			}
			if strings.Contains(got, previewerrors.CartFeatureNote) != tt.wantCart {
				t.Errorf("cart note present = %v, want %v", !tt.wantCart, tt.wantCart)
			}
		})
	}
}

func TestCheckAndRepair_UnrepairedReversionIsExplained(t *testing.T) {
	reverted := strings.Replace(reversionSaved, "padding: 16px;", "padding: 16px;\n  z-index: 10;", 1)
	gen := &promptRecordingGenerator{fakeGenerator: fakeGenerator{results: []*ai.Result{reversionResult(reverted)}}}
	svc := &Service{gen: gen, store: mapThemeStore{files: map[string]string{reversionPath: reversionSaved}}}
	in := GenerateInput{TenantID: 1, ThemeSlug: "demo", Prompt: "still not working", draft: map[string]string{reversionPath: reversionDraft}}

	_, _, err := svc.checkAndRepair(context.Background(), in, "chat-1", "gen-1", ai.ThemeContext{}, nil, reversionResult(reverted), testSnapshot(), nil, nil, nil)
	if !errors.Is(err, ai.ErrDraftReversionUnrepaired) {
		t.Fatalf("expected ErrDraftReversionUnrepaired once repairs ran out, got %v", err)
	}
	msg := ai.SanitizeError(err)
	if !strings.Contains(msg, "I couldn't make this change without undoing your earlier unsaved changes.") ||
		strings.Contains(msg, "couldn't be validated") {
		t.Errorf("expected the unsaved-changes message, got %q", msg)
	}
}

func TestPromptWithAttachments_FollowUpKeepsCartNote(t *testing.T) {
	in := GenerateInput{Prompt: "Still not working", earlierPrompts: []string{"The add to cart button does nothing, fix it"}}
	if got := promptWithAttachments(in.Prompt, in); !strings.Contains(got, previewerrors.CartFeatureNote) {
		t.Errorf("expected the follow-up to carry the cart note, got %q", got)
	}
}

func TestEarlierUserPrompts(t *testing.T) {
	msgs := []chat.Message{
		{ID: "1", Role: chat.RoleUser, Content: "make the header dark"},
		{ID: "2", Role: chat.RoleAssistant, Content: "Done."},
		{ID: "3", Role: chat.RoleUser, Content: "fix the cart"},
		{ID: "4", Role: chat.RoleUser, Content: "still not working"},
		{ID: "5", Role: chat.RoleUser, Content: "Still not working"},
	}
	got := earlierUserPrompts(msgs, "5")
	want := []string{"still not working", "fix the cart", "make the header dark"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("earlierUserPrompts = %q, want %q (newest first, current excluded)", got, want)
	}
}
