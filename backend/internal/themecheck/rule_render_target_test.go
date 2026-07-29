package themecheck

import "testing"

func TestCheckRenderTargetExists_ExistsInSnapshot(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% render 'components/testimonials', theme: theme %}"}}}
	snap := Snapshot{Paths: map[string]bool{"components/testimonials.liquid": true}}
	if got := checkRenderTargetExists(p, snap); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckRenderTargetExists_ExistsInProposal(t *testing.T) {
	p := Proposal{Files: []ProposedFile{
		{Path: "pages/offers.liquid", Content: "{% render 'components/new-thing', theme: theme %}"},
		{Path: "components/new-thing.liquid", Content: "<div></div>"},
	}}
	if got := checkRenderTargetExists(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings when the target is created in the same proposal, got %+v", got)
	}
}

func TestCheckRenderTargetExists_MissingTarget(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% render 'components/nonexistent', theme: theme %}"}}}
	got := checkRenderTargetExists(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckRenderTargetExists_BareName(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "{% render 'testimonials', theme: theme %}"}}}
	got := checkRenderTargetExists(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a bare render path, got %+v", got)
	}
}

func TestCheckRenderTargetExists_IgnoresNonLiquidFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: "{% render 'nope' %}"}}}
	if got := checkRenderTargetExists(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-.liquid files to be ignored, got %+v", got)
	}
}
