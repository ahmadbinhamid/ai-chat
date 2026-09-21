package themebuild

import (
	"testing"
	"time"

	"ai-chat/internal/builderplan"
)

func TestActiveTarget_ResolveFollowUp(t *testing.T) {
	t.Parallel()
	c := newActiveTargetCache()
	c.put(1, "chat-1", activeBuilderTarget{Type: "page", Identity: "blog", Path: "pages/blog.liquid", Operation: "update_page_content"})
	got, ok := c.get(1, "chat-1")
	if !ok || got.Identity != "blog" {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
	plan := builderplan.BuilderPlan{Intent: builderplan.IntentPageTroubleshoot, Operations: []builderplan.Operation{{
		Kind: builderplan.OpDiagnoseExistingPage, ProtectExisting: true,
	}}}
	slug := resolveTroubleshootSlug("now it is not working", plan, got)
	if slug != "blog" {
		t.Fatalf("slug=%q want blog from active target", slug)
	}
}

func TestActiveTarget_NamedPromptWins(t *testing.T) {
	t.Parallel()
	active := activeBuilderTarget{Identity: "blog", Path: "pages/blog.liquid"}
	plan, err := builderplan.BuildPlan("why is the pricing page not opening?")
	if err != nil {
		t.Fatal(err)
	}
	slug := resolveTroubleshootSlug("why is the pricing page not opening?", plan, active)
	if slug != "pricing" {
		t.Fatalf("slug=%q want pricing", slug)
	}
}

func TestShouldShortCircuit_TroubleshootNotClarify(t *testing.T) {
	t.Parallel()
	plan, err := builderplan.BuildPlan("blog page is not working please check and fix it")
	if err != nil {
		t.Fatal(err)
	}
	obs := planObservation{Plan: plan, Valid: true, NeedsDeepSeek: builderplan.NeedsDeepSeek(plan)}
	if shouldShortCircuitClarification(obs) {
		t.Fatal("troubleshoot must not clarification-short-circuit")
	}
	_ = time.Now()
}
