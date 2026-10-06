package themecheck

import "testing"

func layoutStartWithLink(assetPath string) string {
	return `<html><head><link rel="stylesheet" href="{{ '` + assetPath + `' | asset_url }}"></head><body>`
}

func layoutEndWithScripts(assetPaths ...string) string {
	out := "<main></main>"
	for _, p := range assetPaths {
		out += `<script src="{{ '` + p + `' | asset_url }}" defer></script>`
	}
	return out + "</body></html>"
}

func TestCheckAssetRegistered_CSSAlreadyLinked(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: ".x{}"}}}
	snap := Snapshot{Files: map[string]string{"liquid/layout-start.liquid": layoutStartWithLink("pages/css/offers.css")}}
	if got := checkAssetRegistered(p, snap); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckAssetRegistered_CSSAddedThisTurn(t *testing.T) {
	p := Proposal{
		Files:            []ProposedFile{{Path: "components/css/new-thing.css", Content: ".x{}"}},
		LayoutLinksToAdd: []string{"components/css/new-thing.css"},
	}
	if got := checkAssetRegistered(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckAssetRegistered_CSSMissing(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "pages/css/offers.css", Content: ".x{}"}}}
	got := checkAssetRegistered(p, Snapshot{})
	if len(got) != 1 || got[0].Severity != SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckAssetRegistered_JSMissing(t *testing.T) {
	p := Proposal{Files: []ProposedFile{{Path: "js/offers-filter.js", Content: "(function(){})();"}}}
	got := checkAssetRegistered(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding, got %+v", got)
	}
}

func TestCheckAssetRegistered_JSRegisteredNoAPICall(t *testing.T) {
	p := Proposal{
		Files:              []ProposedFile{{Path: "js/offers-filter.js", Content: "(function(){})();"}},
		LayoutScriptsToAdd: []string{"js/offers-filter.js"},
	}
	if got := checkAssetRegistered(p, Snapshot{}); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckAssetRegistered_JSDependsOnAPIAndOrderedAfter(t *testing.T) {
	p := Proposal{
		Files: []ProposedFile{{Path: "js/offers-filter.js", Content: "window.StorefrontApi.get('/x');"}},
	}
	snap := Snapshot{Files: map[string]string{
		"liquid/layout-end.liquid": layoutEndWithScripts("js/storefront-api.js", "js/offers-filter.js"),
	}}
	if got := checkAssetRegistered(p, snap); len(got) != 0 {
		t.Errorf("expected no findings, got %+v", got)
	}
}

func TestCheckAssetRegistered_JSDependsOnAPIButOrderedBefore(t *testing.T) {
	p := Proposal{
		Files: []ProposedFile{{Path: "js/offers-filter.js", Content: "window.StorefrontApi.get('/x');"}},
	}
	snap := Snapshot{Files: map[string]string{
		"liquid/layout-end.liquid": layoutEndWithScripts("js/offers-filter.js", "js/storefront-api.js"),
	}}
	got := checkAssetRegistered(p, snap)
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding for wrong script order, got %+v", got)
	}
}

func TestCheckAssetRegistered_JSDependsOnAPIButAPIMissing(t *testing.T) {
	p := Proposal{
		Files:              []ProposedFile{{Path: "js/offers-filter.js", Content: "window.StorefrontApi.get('/x');"}},
		LayoutScriptsToAdd: []string{"js/offers-filter.js"},
	}
	got := checkAssetRegistered(p, Snapshot{})
	if len(got) != 1 {
		t.Fatalf("expected 1 error finding when storefront-api.js is missing entirely, got %+v", got)
	}
}

// A cache-busting ?v= query after the asset_url output must still count as registered, for links and scripts alike.
func TestCheckAssetRegistered_CacheBusterQuery(t *testing.T) {
	layoutStart := `<html><head><link rel="stylesheet" href="{{ 'pages/css/offers.css' | asset_url }}?v=ssr-cats-3"></head><body>`
	layoutEnd := `<main></main>` +
		`<script src="{{ 'js/storefront-api.js' | asset_url }}?v=ssr-cats-3" defer></script>` +
		`<script src="{{ 'js/minicart.js' | asset_url }}?v=2&amp;x=1" defer></script></body></html>`
	snap := Snapshot{Files: map[string]string{"liquid/layout-start.liquid": layoutStart, "liquid/layout-end.liquid": layoutEnd}}

	tests := []struct {
		name string
		file ProposedFile
	}{
		{"stylesheet with ?v=", ProposedFile{Path: "pages/css/offers.css", Content: ".x{}"}},
		{"script after a cache-busted storefront-api.js", ProposedFile{Path: "js/minicart.js", Content: "(function(){ window.StorefrontApi.getBasket(); })();"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkAssetRegistered(Proposal{Files: []ProposedFile{tt.file}}, snap); len(got) != 0 {
				t.Errorf("expected no findings, got %+v", got)
			}
		})
	}
	if got := registeredAssetPaths(layoutEnd, scriptSrcRe); len(got) != 2 || got[0] != "js/storefront-api.js" || got[1] != "js/minicart.js" {
		t.Errorf("expected both scripts with their paths unchanged, got %v", got)
	}
	if links, scripts, any := AutoFixMissingAssetRegistration(Proposal{Files: []ProposedFile{{Path: "js/minicart.js", Content: "x"}}}, snap); any {
		t.Errorf("auto-fixer must not re-register an already cache-busted script, got links %v scripts %v", links, scripts)
	}
}
