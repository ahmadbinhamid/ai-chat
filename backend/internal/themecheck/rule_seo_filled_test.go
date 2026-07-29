package themecheck

import (
	"testing"

	"ai-chat/internal/themefs"
)

func TestCheckSEOFilled_AllFilled(t *testing.T) {
	p := Proposal{PageRegistryEntry: &themefs.PageEntry{
		SEOTitle: "Offers & Deals | Numbing Cream Co", SEODescription: "Save on numbing creams and gels.", SEOKeywords: "numbing cream, offers",
	}}
	if got := checkSEOFilled(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckSEOFilled_Empty(t *testing.T) {
	p := Proposal{PageRegistryEntry: &themefs.PageEntry{SEOTitle: "Real Title", SEODescription: "", SEOKeywords: "kw"}}
	got := checkSEOFilled(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 warning finding for an empty field, got %+v", got)
	}
}

func TestCheckSEOFilled_Placeholders(t *testing.T) {
	for _, placeholder := range []string{"...", "TODO", "Lorem", "Lorem ipsum", "lorem"} {
		p := Proposal{PageRegistryEntry: &themefs.PageEntry{SEOTitle: placeholder, SEODescription: "real", SEOKeywords: "real"}}
		got := checkSEOFilled(p, Snapshot{})
		if len(got) != 1 {
			t.Errorf("placeholder %q: expected 1 finding, got %+v", placeholder, got)
		}
	}
}

func TestCheckSEOFilled_NoEntry(t *testing.T) {
	if got := checkSEOFilled(Proposal{}, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings when no page is being registered, got %+v", got)
	}
}
