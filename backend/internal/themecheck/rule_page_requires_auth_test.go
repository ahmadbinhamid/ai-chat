package themecheck

import (
	"testing"

	"ai-chat/internal/themefs"
)

func TestCheckPageRequiresAuth_NotSetIsFine(t *testing.T) {
	p := Proposal{PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "offers", Path: "/pages", Type: "custom"}}
	if got := checkPageRequiresAuth(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckPageRequiresAuth_SetIsRejected(t *testing.T) {
	p := Proposal{PageRegistryEntry: &themefs.PageEntry{Page: "offers", Slug: "offers", Path: "/pages", Type: "custom", RequiresAuth: true}}
	got := checkPageRequiresAuth(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError || got[0].Rule != ruleIDPageRequiresAuth {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckPageRequiresAuth_NoEntry(t *testing.T) {
	if got := checkPageRequiresAuth(Proposal{}, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings when no page is being registered, got %+v", got)
	}
}
