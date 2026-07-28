package themefs

import (
	"strings"
	"testing"
)

const sampleLayoutStart = `<!DOCTYPE html>
<html lang="en">
<head>
  <link rel="stylesheet" href="{{ 'css/base.css' | asset_url }}">
</head>
<body>
`

const sampleLayoutEnd = `</main>
<script src="{{ 'js/theme.js' | asset_url }}" defer></script>
</body>
</html>
`

func TestAddStylesheetLink_InsertsBeforeHeadClose(t *testing.T) {
	updated, changed, err := AddStylesheetLink(sampleLayoutStart, "pages/css/offers.css")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first insertion")
	}
	if !containsBefore(updated, "pages/css/offers.css", "</head>") {
		t.Fatalf("expected new link before </head>, got:\n%s", updated)
	}
}

func TestAddStylesheetLink_Idempotent(t *testing.T) {
	first, _, err := AddStylesheetLink(sampleLayoutStart, "pages/css/offers.css")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, changed, err := AddStylesheetLink(first, "pages/css/offers.css")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on re-applying an already-present link")
	}
	if first != second {
		t.Fatal("expected content to be unchanged when the link is already present")
	}
}

func TestAddStylesheetLink_MissingMarker(t *testing.T) {
	if _, _, err := AddStylesheetLink("no head tag here", "x.css"); err == nil {
		t.Fatal("expected an error when </head> is missing")
	}
}

func TestAddDeferredScript_InsertsBeforeBodyClose(t *testing.T) {
	updated, changed, err := AddDeferredScript(sampleLayoutEnd, "js/offers-filter.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first insertion")
	}
	if !containsBefore(updated, "js/offers-filter.js", "</body>") {
		t.Fatalf("expected new script before </body>, got:\n%s", updated)
	}
}

func TestAddDeferredScript_Idempotent(t *testing.T) {
	first, _, _ := AddDeferredScript(sampleLayoutEnd, "js/offers-filter.js")
	second, changed, err := AddDeferredScript(first, "js/offers-filter.js")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on re-applying an already-present script tag")
	}
	if first != second {
		t.Fatal("expected content to be unchanged when the script is already present")
	}
}

// containsBefore reports whether needle appears in s at an index earlier
// than marker's first occurrence.
func containsBefore(s, needle, marker string) bool {
	ni := strings.Index(s, needle)
	mi := strings.Index(s, marker)
	return ni != -1 && mi != -1 && ni < mi
}
