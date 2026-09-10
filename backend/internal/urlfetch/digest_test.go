package urlfetch

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// loadDigestFixture reads testdata/<name>.html (required) and
// testdata/<name>.css (optional — some fixtures, like inline_style_only
// and client_rendered_shell, deliberately have none), then combines the
// external CSS with whatever inline <style> content the HTML itself
// contains — mirroring how Service.fetchReferenceURL actually assembles
// BuildDigest's css argument in production (Fetcher.FetchStylesheets
// combining inline + fetched-external CSS), just without a real network
// fetch standing in for "external".
func loadDigestFixture(t *testing.T, name string) (htmlSrc, css string) {
	t.Helper()
	htmlBytes, err := os.ReadFile(filepath.Join("testdata", name+".html"))
	if err != nil {
		t.Fatalf("failed to read fixture html: %v", err)
	}
	htmlSrc = string(htmlBytes)

	finalURL, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	_, inlineCSS := extractStylesheetSources(htmlSrc, finalURL)

	var externalCSS string
	if b, err := os.ReadFile(filepath.Join("testdata", name+".css")); err == nil {
		externalCSS = string(b)
	} else if !os.IsNotExist(err) {
		t.Fatalf("failed to read fixture css: %v", err)
	}

	return htmlSrc, inlineCSS + "\n" + externalCSS
}

func TestBuildDigest_OrdinaryMarketingPage(t *testing.T) {
	htmlSrc, css := loadDigestFixture(t, "marketing_page")
	finalURL, _ := url.Parse("https://acme-outdoors.example.com/")
	d := BuildDigest(finalURL, htmlSrc, css)

	if d.Empty {
		t.Errorf("expected a real marketing page to NOT be classified Empty, got digest:\n%s", d.Text)
	}
	if d.Title != "Acme Outdoors — Gear for the trail" {
		t.Errorf("Title = %q", d.Title)
	}
	// #6E9A3A is background-color in 3 of the fixture CSS's rules — the
	// single most-repeated background-color, i.e. exactly the "brand
	// color" signal design tokens ranking exists to surface.
	if !strings.Contains(d.Text, "#6E9A3A") {
		t.Errorf("expected the brand color #6E9A3A to appear in the digest:\n%s", d.Text)
	}
	// "Georgia" is the fixture's heading typeface (h1, h2, h3 { font-family:
	// "Georgia", serif; ... }) — the "names the page's actual typefaces"
	// half of Phase 3's own "Done when" criterion.
	if !strings.Contains(d.Text, "Georgia") {
		t.Errorf("expected the heading typeface \"Georgia\" to appear in the digest:\n%s", d.Text)
	}
	if !strings.Contains(d.Text, "Gear built for the trail") {
		t.Errorf("expected the h1 text to appear in the digest:\n%s", d.Text)
	}
	for _, navLabel := range []string{"Home", "About", "Cart (0)"} {
		if !strings.Contains(d.Text, navLabel) {
			t.Errorf("expected nav label %q to appear in the digest:\n%s", navLabel, d.Text)
		}
	}
	if len(d.Text) > digestHardCapBytes {
		t.Errorf("digest exceeds hard cap: %d bytes", len(d.Text))
	}
	if d.Truncated {
		t.Error("expected an ordinary, well-under-cap page to NOT be classified Truncated")
	}
}

func TestBuildDigest_CSSCustomProperties(t *testing.T) {
	htmlSrc, css := loadDigestFixture(t, "custom_properties")
	finalURL, _ := url.Parse("https://example.com/")
	d := BuildDigest(finalURL, htmlSrc, css)

	if !strings.Contains(d.Text, "--brand-green: #6E9A3A") {
		t.Errorf("expected the custom property verbatim (name AND value) in the digest:\n%s", d.Text)
	}
	if !strings.Contains(d.Text, "--brand-navy: #1B2A4A") {
		t.Errorf("expected a second custom property verbatim in the digest:\n%s", d.Text)
	}
}

func TestBuildDigest_ClientRenderedShellIsEmpty(t *testing.T) {
	htmlSrc, css := loadDigestFixture(t, "client_rendered_shell")
	finalURL, _ := url.Parse("https://example.com/")
	d := BuildDigest(finalURL, htmlSrc, css)

	if !d.Empty {
		t.Errorf("expected a client-rendered shell (no headings, no landmarks, no real copy) to be classified Empty, got digest:\n%s", d.Text)
	}
}

func TestBuildDigest_InlineStyleOnlyNoExternalStylesheet(t *testing.T) {
	htmlSrc, css := loadDigestFixture(t, "inline_style_only")
	finalURL, _ := url.Parse("https://example.com/")
	d := BuildDigest(finalURL, htmlSrc, css)

	if d.Empty {
		t.Errorf("expected real headings and copy to NOT be classified Empty, got digest:\n%s", d.Text)
	}
	// #B5651D is declared only inside the fixture's <style> block — there
	// is no external stylesheet for this fixture at all (no
	// testdata/inline_style_only.css) — so this only passes if inline
	// <style> content actually reaches BuildDigest's design-tokens
	// extraction.
	if !strings.Contains(d.Text, "#B5651D") {
		t.Errorf("expected a color declared only in an inline <style> block to appear in the digest:\n%s", d.Text)
	}
}

func TestBuildDigest_OverHardCapIsTruncated(t *testing.T) {
	htmlSrc, css := loadDigestFixture(t, "over_hard_cap")
	finalURL, _ := url.Parse("https://example.com/")
	d := BuildDigest(finalURL, htmlSrc, css)

	if len(d.Text) > digestHardCapBytes {
		t.Fatalf("expected the digest to be truncated to at most %d bytes, got %d", digestHardCapBytes, len(d.Text))
	}
	// Close to (not just under) the cap — proves truncation actually fired
	// rather than the content happening to land under the cap on its own,
	// which per-section caps alone would not achieve for this fixture (see
	// its own generation comment / size).
	if len(d.Text) < digestHardCapBytes-10 {
		t.Errorf("expected the digest to be truncated close to the %d-byte cap, got %d bytes — did the fixture stop being large enough to actually exceed it?", digestHardCapBytes, len(d.Text))
	}
	if !utf8.ValidString(d.Text) {
		t.Error("expected the hard-cap-truncated digest to still be valid UTF-8")
	}
	if d.Empty {
		t.Error("expected a genuinely content-rich (if oversized) page to NOT be classified Empty")
	}
	if !d.Truncated {
		t.Error("expected Truncated to be true when the hard-cap truncation actually fired")
	}
}

// TestCSSPropertyPattern_DoesNotMatchAsASuffixOfALongerProperty guards the
// exact bug class cssPropertyPatterns' own doc comment describes: "color"
// naively matched via \bcolor\b also matches inside "background-color"
// because CSS's "-" is a regex word-boundary character. This asserts the
// "color" pattern captures NOTHING from a rule that only declares
// background-color, and the "background-color" pattern captures correctly
// from the same input.
func TestCSSPropertyPattern_DoesNotMatchAsASuffixOfALongerProperty(t *testing.T) {
	css := ".btn { background-color: #6E9A3A; }"
	if m := cssPropertyPatterns["color"].FindStringSubmatch(css); m != nil {
		t.Errorf("expected the bare \"color\" pattern to NOT match inside \"background-color\", got match: %v", m)
	}
	m := cssPropertyPatterns["background-color"].FindStringSubmatch(css)
	if m == nil || normalizeCSSValue(m[1]) != "#6E9A3A" {
		t.Errorf("expected \"background-color\" pattern to capture #6E9A3A, got: %v", m)
	}
}

// TestCSSPropertyPattern_MatchesFirstDeclarationInARule confirms the
// pattern's "preceded by { or ; " requirement still matches a property
// that's the very first declaration after an opening brace, with no
// preceding ";" to anchor on.
func TestCSSPropertyPattern_MatchesFirstDeclarationInARule(t *testing.T) {
	css := ".btn{color:red;background:blue}"
	m := cssPropertyPatterns["color"].FindStringSubmatch(css)
	if m == nil || normalizeCSSValue(m[1]) != "red" {
		t.Errorf("expected \"color\" pattern to capture \"red\" as the rule's first declaration, got: %v", m)
	}
}

func TestFilenameStem(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"plain filename", "hero-banner.webp", "hero-banner"},
		{"full URL with hash and extension", "https://cdn.example.com/img/hero-banner.abc123.webp", "hero-banner.abc123"},
		{"query string stripped", "https://cdn.example.com/hero.webp?w=800&fmt=auto", "hero"},
		{"fragment stripped", "https://cdn.example.com/hero.webp#anchor", "hero"},
		{"no extension", "https://cdn.example.com/img/hero-banner", "hero-banner"},
		{"empty src", "", ""},
		{"trailing slash, no filename", "https://cdn.example.com/img/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filenameStem(tt.src); got != tt.want {
				t.Errorf("filenameStem(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestTruncateBytes_PreservesValidUTF8(t *testing.T) {
	// "é" is 2 bytes (0xC3 0xA9) — cutting at n=1 lands mid-rune.
	s := "aé"
	got := truncateBytes(s, 1)
	if !utf8.ValidString(got) {
		t.Errorf("truncateBytes(%q, 1) = %q, not valid UTF-8", s, got)
	}
	if got != "a" {
		t.Errorf("truncateBytes(%q, 1) = %q, want %q", s, got, "a")
	}
}

func TestCollapseWhitespace(t *testing.T) {
	got := collapseWhitespace("  Home\n    Page  \t here  ")
	want := "Home Page here"
	if got != want {
		t.Errorf("collapseWhitespace(...) = %q, want %q", got, want)
	}
}
