package themecheck

import "testing"

func TestCheckNoFramework_CleanFilesPass(t *testing.T) {
	p := Proposal{Files: []ProposedFile{
		{Path: "pages/offers.liquid", Content: `<div class="page-hero t1-pd-title"><a class="btn-primary" href="/products">Shop</a></div>`},
		{Path: "components/css/testimonials.css", Content: ".t1-tm-card { display: grid; grid-template-columns: 1fr; }"},
		{Path: "js/offers-filter.js", Content: "(function(){ var root = document.querySelector('[data-of-root]'); if (!root) return; })();"},
	}}
	if got := checkNoFramework(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings on clean theme-native files, got %+v", got)
	}
}

func TestCheckNoFramework_TailwindResponsivePrefix(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: `<div class="flex md:flex-row">x</div>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding for a Tailwind responsive prefix, got %+v", got)
	}
}

func TestCheckNoFramework_TailwindArbitraryValue(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: `<div class="text-[14px]">x</div>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for Tailwind arbitrary-value syntax, got %+v", got)
	}
}

func TestCheckNoFramework_BootstrapBtnPair(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: `<a class="btn btn-primary">Shop</a>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for Bootstrap's btn/btn-primary pairing, got %+v", got)
	}
}

func TestCheckNoFramework_BootstrapDataAttr(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: `<div data-bs-toggle="modal">x</div>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a Bootstrap data attribute, got %+v", got)
	}
}

func TestCheckNoFramework_React(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/widget.js", Content: "import React from 'react';"}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) == 0 {
		t.Fatalf("expected at least 1 finding for a React import")
	}
}

func TestCheckNoFramework_VueDirective(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: `<div v-if="show">x</div>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a Vue directive, got %+v", got)
	}
}

func TestCheckNoFramework_JQuery(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/widget.js", Content: "$(document).ready(function(){});"}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a jQuery ready idiom, got %+v", got)
	}
}

func TestCheckNoFramework_BuildToolConfig(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/tailwind.config.js", Content: "module.exports = {};"}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a build-tool config file, got %+v", got)
	}
}

func TestCheckNoFramework_ExternalScriptSrc(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "liquid/layout-end.liquid", Content: `<script src="https://cdn.example.com/lib.js"></script>`}}}
	got := checkNoFramework(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for an external script src, got %+v", got)
	}
}

func TestCheckNoFramework_AssetURLScriptSrcIsFine(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "liquid/layout-end.liquid", Content: `<script src="{{ 'js/theme.js' | asset_url }}" defer></script>`}}}
	if got := checkNoFramework(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings for a normal asset_url script tag, got %+v", got)
	}
}
