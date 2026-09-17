package ai

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestResolveEffort(t *testing.T) {
	cases := []struct {
		name string
		tc   ThemeContext
		cfg  anthropic.OutputConfigEffort
		want anthropic.OutputConfigEffort
	}{
		{"edit default medium", ThemeContext{}, "", anthropic.OutputConfigEffortMedium},
		{"edit honors AI_EFFORT=low", ThemeContext{}, anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortLow},
		{"edit honors medium", ThemeContext{}, anthropic.OutputConfigEffortMedium, anthropic.OutputConfigEffortMedium},
		{"edit demotes env xhigh to high", ThemeContext{}, anthropic.OutputConfigEffortXhigh, anthropic.OutputConfigEffortHigh},
		{"brand always low", ThemeContext{GenerationMode: GenerationModeBrand}, anthropic.OutputConfigEffortHigh, anthropic.OutputConfigEffortLow},
		{"copy honors low", ThemeContext{GenerationMode: GenerationModeCopy}, anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortLow},
		{"pages default high", ThemeContext{GenerationMode: GenerationModePages}, "", anthropic.OutputConfigEffortHigh},
		{"pages honors low", ThemeContext{GenerationMode: GenerationModePages}, anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortLow},
		{"repair forces medium", ThemeContext{Repair: true}, anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortMedium},
		{"override wins", ThemeContext{EffortOverride: "high"}, anthropic.OutputConfigEffortLow, anthropic.OutputConfigEffortHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveEffort(tc.tc, tc.cfg); got != tc.want {
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
