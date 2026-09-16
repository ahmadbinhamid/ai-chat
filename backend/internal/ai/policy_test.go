package ai

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestResolveEffort(t *testing.T) {
	cfg := anthropic.OutputConfigEffortXhigh
	cases := []struct {
		name string
		tc   ThemeContext
		want anthropic.OutputConfigEffort
	}{
		{"edit", ThemeContext{}, anthropic.OutputConfigEffortMedium},
		{"brand", ThemeContext{GenerationMode: GenerationModeBrand}, anthropic.OutputConfigEffortLow},
		{"copy", ThemeContext{GenerationMode: GenerationModeCopy}, anthropic.OutputConfigEffortMedium},
		{"pages", ThemeContext{GenerationMode: GenerationModePages}, anthropic.OutputConfigEffortHigh},
		{"repair", ThemeContext{Repair: true}, anthropic.OutputConfigEffortMedium},
		{"override", ThemeContext{EffortOverride: "high"}, anthropic.OutputConfigEffortHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveEffort(tc.tc, cfg); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveMaxTokens(t *testing.T) {
	budgets := DefaultTokenBudgets()
	cases := []struct {
		name string
		tc   ThemeContext
		cfg  int64
		want int64
	}{
		{"edit under ceiling", ThemeContext{}, 64000, budgets.Interactive},
		{"pages", ThemeContext{GenerationMode: GenerationModePages}, 64000, budgets.Complex},
		{"repair", ThemeContext{Repair: true}, 64000, budgets.Repair},
		{"brand", ThemeContext{GenerationMode: GenerationModeBrand}, 64000, budgets.Brand},
		{"respects low ceiling", ThemeContext{}, 4000, 4000},
		{"override", ThemeContext{MaxTokensOverride: 12000}, 64000, 12000},
		{"simple_edit one-shot", ThemeContext{SimpleEditOneShot: true}, 64000, 8000},
		{"simple_edit override wins", ThemeContext{SimpleEditOneShot: true, MaxTokensOverride: 5000}, 64000, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMaxTokens(tc.tc, tc.cfg, budgets); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestCompactReadThemeFileResult_OmitsRepeats(t *testing.T) {
	counts := map[string]int{}
	input := []byte(`{"paths":["pages/home.liquid"]}`)
	out1, force := compactReadThemeFileResult("### pages/home.liquid\nBODY\n\n", input, counts, false)
	if force || !contains(out1, "BODY") {
		t.Fatalf("first read should keep body, force=%v out=%q", force, out1)
	}
	out2, force := compactReadThemeFileResult("### pages/home.liquid\nBODY\n\n", input, counts, false)
	if !contains(out2, "omitted") {
		t.Fatalf("second read should omit body, got %q", out2)
	}
	_, force = compactReadThemeFileResult("### pages/home.liquid\nBODY\n\n", input, counts, false)
	if !force {
		t.Fatal("third read should force propose")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
