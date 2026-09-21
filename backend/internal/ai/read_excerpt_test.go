package ai

import (
	"strings"
	"testing"
)

func TestExcerptReadThemeFileResult_LargeFile(t *testing.T) {
	var lines []string
	lines = append(lines, `<header class="site-header">`)
	for i := 0; i < 400; i++ {
		lines = append(lines, "  <div class=\"row\">item</div>")
	}
	lines = append(lines, `</header>`)
	body := strings.Join(lines, "\n")
	raw := "### components/header.liquid\n" + body + "\n\n"
	out := excerptReadThemeFileResult(raw)
	if !strings.Contains(out, "excerpt") {
		t.Fatalf("expected excerpt marker, got %d chars", len(out))
	}
	if !strings.Contains(out, "Structural outline") {
		t.Fatalf("expected outline section")
	}
	if strings.Count(out, "\n") >= strings.Count(raw, "\n") {
		t.Fatalf("excerpt should be smaller than original")
	}
	// Small files pass through unchanged.
	small := "### pages/home.liquid\nhello\n\n"
	if excerptReadThemeFileResult(small) != small {
		t.Fatalf("small file should be unchanged")
	}
}
