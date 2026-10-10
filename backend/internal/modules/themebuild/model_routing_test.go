package themebuild

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/aicatalog"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/previewerrors"
)

// modelRecordingGenerator answers every turn without changes and records the model each call was asked to use.
type modelRecordingGenerator struct {
	fakeGenerator
	mu        sync.Mutex
	models    []aicatalog.Choice
	redesigns []bool
}

func (g *modelRecordingGenerator) Generate(_ context.Context, tc ai.ThemeContext, _ []ai.Turn, _ string, _ []ai.Image, _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	g.models = append(g.models, tc.Model)
	g.redesigns = append(g.redesigns, tc.Redesign)
	g.mu.Unlock()
	return &ai.Result{Summary: "Checked it.", AnsweredQuestion: true, ExplorationToolCalls: 1, ModelID: tc.Model.ModelID, Effort: tc.Model.Effort}, nil
}

func committedCatalogue(t *testing.T) *aicatalog.Catalog {
	t.Helper()
	data, err := os.ReadFile("../../../config/ai-models.deepseek.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := aicatalog.Parse(data, func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestGenerate_AutoRoutesAndRecordsTheModel(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	store := mapThemeStore{files: map[string]string{
		"pages.json": "[]", "defaults.json": "{}",
		"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
	}}
	svc := NewService(NewRepository(conn), chatSvc, nil, store, nil)
	gen := &modelRecordingGenerator{}
	svc.gen = gen
	svc.SetModelCatalog(committedCatalogue(t))
	tenantID := uint64(time.Now().UnixNano())

	turns := []struct {
		prompt, model, effort string
		want                  aicatalog.Choice
	}{
		{prompt: "make the header dark", want: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}},
		{prompt: "the add to cart button does nothing", want: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "low"}},
		{prompt: "still not working", want: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "low"}},
		{prompt: "make the footer blue", model: "deepseek-pro", effort: "high", want: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "high"}},
	}
	var chatID string
	for i, turn := range turns {
		outcome, err := svc.Generate(context.Background(), GenerateInput{
			TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "demo",
			Prompt: turn.prompt, ModelID: turn.model, Effort: turn.effort,
		})
		if err != nil {
			t.Fatalf("Generate(%q): %v", turn.prompt, err)
		}
		chatID = outcome.Chat.ID
		waitForAssistantReplies(t, chatSvc, tenantID, chatID, i+1)
	}

	if len(gen.models) != len(turns) {
		t.Fatalf("expected %d generator calls, got %d", len(turns), len(gen.models))
	}
	messages, err := chatSvc.ListMessages(context.Background(), tenantID, chatID)
	if err != nil {
		t.Fatal(err)
	}
	var replies []chat.Message
	for _, m := range messages {
		if m.Role == chat.RoleAssistant {
			replies = append(replies, m)
		}
	}
	for i, turn := range turns {
		if gen.models[i] != turn.want {
			t.Errorf("%q ran on %+v, want %+v", turn.prompt, gen.models[i], turn.want)
		}
		if r := replies[i]; r.ModelID == nil || *r.ModelID != turn.want.ModelID || r.Effort == nil || *r.Effort != turn.want.Effort {
			t.Errorf("%q reply recorded model %v/%v, want %+v", turn.prompt, r.ModelID, r.Effort, turn.want)
		}
		if replies[i].Content != "Checked it." {
			t.Errorf("the model must never appear in the merchant-facing content, got %q", replies[i].Content)
		}
	}
}

// waitForAssistantReplies polls until the chat has n assistant messages.
func waitForAssistantReplies(t *testing.T, chatSvc *chat.Service, tenantID uint64, chatID string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := chatSvc.ListMessages(context.Background(), tenantID, chatID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, m := range messages {
			if m.Role == chat.RoleAssistant {
				count++
			}
		}
		if count >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d assistant replies", n)
}

// Auto sends a redesign turn to redesign_model with the brief; a model the merchant picked keeps running but still
// gets the brief; an ordinary turn, and a fix that also asks for a redesign, get neither. The flag is stored on the generation the queued turn runs from.
func TestGenerate_RoutesRedesignTurns(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	store := mapThemeStore{files: map[string]string{
		"pages.json": "[]", "defaults.json": "{}",
		"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
	}}
	repo := NewRepository(conn)
	svc := NewService(repo, chatSvc, nil, store, nil)
	gen := &modelRecordingGenerator{}
	svc.gen = gen
	data, err := os.ReadFile("../../../config/ai-models.deepseek.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["auto"].(map[string]any)["redesign_model"] = "deepseek-pro"
	raw["auto"].(map[string]any)["redesign_effort"] = "medium"
	data, _ = json.Marshal(raw)
	cat, err := aicatalog.Parse(data, func(string) (string, bool) { return "k", true })
	if err != nil {
		t.Fatal(err)
	}
	svc.SetModelCatalog(cat)
	tenantID := uint64(time.Now().UnixNano())

	turns := []struct {
		prompt, model, effort string
		want                  aicatalog.Choice
		redesign              bool
		previewErrors         bool
	}{
		{prompt: "redesign the homepage", want: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "medium"}, redesign: true},
		{prompt: "make the header dark", want: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}},
		{prompt: "give the store a makeover", model: "deepseek-flash", effort: "high", want: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "high"}, redesign: true},
		// A fix wins over a redesign: no brief, and Auto takes the fix route.
		{prompt: "redesign the homepage", previewErrors: true, want: aicatalog.Choice{ModelID: "deepseek-pro", Effort: "low"}},
		{prompt: "revamp the cart page, checkout is broken and doesn't work", model: "deepseek-flash", effort: "low", want: aicatalog.Choice{ModelID: "deepseek-flash", Effort: "low"}},
	}
	for i, turn := range turns {
		in := GenerateInput{
			TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "demo",
			Prompt: turn.prompt, ModelID: turn.model, Effort: turn.effort,
		}
		if turn.previewErrors {
			in.PreviewErrors = []previewerrors.Entry{{Type: "error", Message: "Uncaught TypeError: x is undefined", Count: 1}}
		}
		outcome, err := svc.Generate(context.Background(), in)
		if err != nil {
			t.Fatalf("Generate(%q): %v", turn.prompt, err)
		}
		waitForAssistantReplies(t, chatSvc, tenantID, outcome.Chat.ID, i+1)
		stored, err := svc.LatestGeneration(context.Background(), outcome.Chat.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Redesign != turn.redesign {
			t.Errorf("%q stored redesign=%v, want %v", turn.prompt, stored.Redesign, turn.redesign)
		}
	}
	if len(gen.models) != len(turns) {
		t.Fatalf("expected %d generator calls, got %d", len(turns), len(gen.models))
	}
	for i, turn := range turns {
		if gen.models[i] != turn.want || gen.redesigns[i] != turn.redesign {
			t.Errorf("%q ran on %+v redesign=%v, want %+v redesign=%v", turn.prompt, gen.models[i], gen.redesigns[i], turn.want, turn.redesign)
		}
	}
}
