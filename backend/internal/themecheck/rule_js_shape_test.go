package themecheck

import "testing"

const wellShapedJS = `(function () {
  'use strict';
  var root = document.querySelector('[data-of-root]');
  if (!root) return;
  root.addEventListener('click', function () {});
})();`

func TestCheckJSShape_WellShaped(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: wellShapedJS}}}
	if got := checkJSShape(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckJSShape_ArrowIIFEIsFine(t *testing.T) {
	content := `(() => {
  const root = document.querySelector('[data-of-root]');
  if (!root) return;
})();`
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: content}}}
	if got := checkJSShape(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckJSShape_NotIIFEWrapped(t *testing.T) {
	content := `var root = document.querySelector('[data-of-root]');
if (!root) return;`
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: content}}}
	got := checkJSShape(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 warning finding for a non-IIFE file, got %+v", got)
	}
}

func TestCheckJSShape_NoRootGuard(t *testing.T) {
	content := `(function () {
  var root = document.querySelector('[data-of-root]');
  root.addEventListener('click', function () {});
})();`
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: content}}}
	got := checkJSShape(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a missing root guard, got %+v", got)
	}
}

func TestCheckJSShape_ClassSelectorInsteadOfDataHook(t *testing.T) {
	content := `(function () {
  var root = document.querySelector('.offers-filter');
  if (!root) return;
})();`
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: content}}}
	got := checkJSShape(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for a class selector used as a JS hook, got %+v", got)
	}
}

func TestCheckJSShape_NoRootQueryNoGuardNeeded(t *testing.T) {
	// A script with no querySelector/getElementById call at all has nothing
	// to guard — shouldn't be penalized for that.
	content := `(function () {
  window.addEventListener('scroll', function () {});
})();`
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: content}}}
	if got := checkJSShape(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckJSShape_IgnoresNonJSFiles(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/offers.liquid", Content: "var x = 1;"}}}
	if got := checkJSShape(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected non-js/*.js files to be ignored, got %+v", got)
	}
}
