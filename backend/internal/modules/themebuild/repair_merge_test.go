package themebuild

import (
	"context"
	"reflect"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

const promoComponent = "<div class=\"promo\">Summer sale</div>"

// A multi-file proposal where only one file is flagged: the repair re-sends just that file, and every other file survives.
func TestCheckAndRepair_UnflaggedFilesSurviveRepair(t *testing.T) {
	original := &ai.Result{
		Summary: "Added a summer promo to the offers page",
		Files: []ai.GeneratedFile{
			{Path: "components/promo.liquid", Action: "create", Content: promoComponent},
			{Path: "pages/offers.liquid", Action: "update", Content: badPageContent},
		},
	}
	repair := &ai.Result{
		Summary: "Fixed the offers page boilerplate",
		Files:   []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: goodPageContent}},
	}
	fg := &fakeGenerator{results: []*ai.Result{repair}}
	svc := &Service{gen: fg}

	got, _, err := svc.checkAndRepair(context.Background(), GenerateInput{TenantID: 1, ThemeSlug: "demo"}, "chat-1", "gen-1", ai.ThemeContext{}, nil, original, testSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("checkAndRepair: %v", err)
	}
	if fg.calls != 1 {
		t.Fatalf("expected exactly 1 repair round, got %d", fg.calls)
	}
	want := []ai.GeneratedFile{
		{Path: "components/promo.liquid", Action: "create", Content: promoComponent},
		{Path: "pages/offers.liquid", Action: "update", Content: goodPageContent},
	}
	if !reflect.DeepEqual(got.Files, want) {
		t.Errorf("expected the unflagged file kept and the flagged one repaired, got %+v", got.Files)
	}
	if got.Summary != original.Summary {
		t.Errorf("expected the original summary, got %q", got.Summary)
	}
}

// The test-run failure: the repair answered an asset check by only re-registering a script, and the real fix was lost.
func TestCheckAndRepair_LayoutOnlyRepairKeepsTheFix(t *testing.T) {
	original := &ai.Result{
		Summary: "Fixed the cart script's missing brace",
		Files:   []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: badPageContent}},
	}
	repair := &ai.Result{
		Summary:            "Registered the script",
		Files:              []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: goodPageContent}},
		LayoutScriptsToAdd: []string{"js/minicart.js"},
	}
	svc := &Service{gen: &fakeGenerator{results: []*ai.Result{repair}}}

	got, _, err := svc.checkAndRepair(context.Background(), GenerateInput{TenantID: 1, ThemeSlug: "demo"}, "chat-1", "gen-1", ai.ThemeContext{}, nil, original, testSnapshot(), nil, nil, nil)
	if err != nil {
		t.Fatalf("checkAndRepair: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0].Content != goodPageContent || !reflect.DeepEqual(got.LayoutScriptsToAdd, []string{"js/minicart.js"}) {
		t.Errorf("expected the repaired file plus the repair's layout registration, got %+v", got)
	}
}

func TestMergeRepairIntoProposal(t *testing.T) {
	entry := &themefs.PageEntry{Slug: "offers", Page: "offers"}
	original := &ai.Result{
		Summary:              "original",
		Files:                []ai.GeneratedFile{{Path: "a.liquid", Content: "a1"}, {Path: "b.liquid", Content: "b1"}},
		PageRegistryEntry:    entry,
		LayoutLinksToAdd:     []string{"pages/css/a.css"},
		LayoutScriptsToAdd:   []string{"js/a.js"},
		ExplorationToolCalls: 3,
	}

	t.Run("same path replaced in place, new path appended, layouts unioned", func(t *testing.T) {
		repair := &ai.Result{
			Summary:              "repair",
			Files:                []ai.GeneratedFile{{Path: "c.liquid", Content: "c2"}, {Path: "a.liquid", Content: "a2"}},
			LayoutLinksToAdd:     []string{"pages/css/a.css", "pages/css/c.css"},
			LayoutScriptsToAdd:   []string{"js/a.js"},
			ExplorationToolCalls: 1,
		}
		got := mergeRepairIntoProposal(original, repair)
		wantFiles := []ai.GeneratedFile{{Path: "a.liquid", Content: "a2"}, {Path: "b.liquid", Content: "b1"}, {Path: "c.liquid", Content: "c2"}}
		if !reflect.DeepEqual(got.Files, wantFiles) {
			t.Errorf("files = %+v, want %+v", got.Files, wantFiles)
		}
		if got.Summary != "original" || got.PageRegistryEntry != entry || got.ExplorationToolCalls != 4 {
			t.Errorf("unexpected merged metadata %+v", got)
		}
		if !reflect.DeepEqual(got.LayoutLinksToAdd, []string{"pages/css/a.css", "pages/css/c.css"}) ||
			!reflect.DeepEqual(got.LayoutScriptsToAdd, []string{"js/a.js"}) {
			t.Errorf("layout registrations not unioned: %v %v", got.LayoutLinksToAdd, got.LayoutScriptsToAdd)
		}
	})

	t.Run("repair's page registry entry wins", func(t *testing.T) {
		newEntry := &themefs.PageEntry{Slug: "deals", Page: "deals"}
		got := mergeRepairIntoProposal(original, &ai.Result{PageRegistryEntry: newEntry})
		if got.PageRegistryEntry != newEntry || len(got.Files) != 2 {
			t.Errorf("unexpected %+v", got)
		}
	})

	t.Run("a clarifying repair replaces the proposal", func(t *testing.T) {
		repair := &ai.Result{Summary: "Which page did you mean?", NeedsClarification: true}
		if got := mergeRepairIntoProposal(original, repair); got != repair {
			t.Errorf("expected the clarification itself, got %+v", got)
		}
	})

	t.Run("original is not mutated", func(t *testing.T) {
		mergeRepairIntoProposal(original, &ai.Result{Files: []ai.GeneratedFile{{Path: "a.liquid", Content: "zzz"}}})
		if original.Files[0].Content != "a1" {
			t.Error("merge must not modify the original proposal")
		}
	})
}

func TestDropUnchangedFiles(t *testing.T) {
	same, changed := "same", "old"
	plan := writePlan{files: []planFile{
		{path: "unchanged.liquid", content: "same", previous: &same},
		{path: "changed.liquid", content: "new", previous: &changed},
		{path: "created.liquid", content: "fresh"},
		{path: "pages/registered.liquid", content: "same", previous: &same, pageMeta: &themefs.PageMeta{Slug: "registered"}},
	}}
	var got []string
	for _, f := range dropUnchangedFiles(plan).files {
		got = append(got, f.path)
	}
	want := []string{"changed.liquid", "created.liquid", "pages/registered.liquid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

// End to end: a proposal whose only file is identical to the theme stages nothing, so the merchant is told nothing
// changed instead of the model's "Fixed", and the turn isn't left pending.
func TestDoGenerate_NoOpProposalReportsNothingChanged(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	store := mapThemeStore{files: map[string]string{
		"pages.json": "[]", "defaults.json": "{}",
		"liquid/layout-start.liquid": "<html><head></head><body>", "liquid/layout-end.liquid": "</body></html>",
		"pages/offers.liquid": goodPageContent,
	}}
	svc := NewService(NewRepository(conn), chatSvc, nil, store, nil)
	svc.gen = &fakeGenerator{results: []*ai.Result{{
		Summary: "Fixed the offers page.",
		Files:   []ai.GeneratedFile{{Path: "pages/offers.liquid", Action: "update", Content: goodPageContent}},
	}}}
	tenantID := uint64(time.Now().UnixNano())

	outcome, err := svc.Generate(context.Background(), GenerateInput{TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "demo", Prompt: "fix the offers page"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	reply := messages[len(messages)-1]
	if reply.Content != nothingChangedReply || reply.ApplyStatus != chat.ApplyStatusNotApplicable {
		t.Errorf("expected the honest no-change reply, not pending; got %q (%s)", reply.Content, reply.ApplyStatus)
	}
	if draft, err := svc.repo.DraftFiles(context.Background(), outcome.Chat.ID); err != nil || len(draft) != 0 {
		t.Errorf("expected nothing staged, got %v (%v)", draft, err)
	}
}
