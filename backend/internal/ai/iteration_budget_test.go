package ai

import "testing"

func TestToolIterationBudget(t *testing.T) {
	cases := []struct {
		name string
		tc   ThemeContext
		want int
	}{
		{"default edit", ThemeContext{}, 12},
		{"brand", ThemeContext{GenerationMode: GenerationModeBrand}, 4},
		{"copy", ThemeContext{GenerationMode: GenerationModeCopy}, 8},
		{"pages", ThemeContext{GenerationMode: GenerationModePages}, 14},
		{"repair", ThemeContext{Repair: true}, 8},
		{"explicit override", ThemeContext{MaxToolIterations: 7}, 7},
		{"ceiling clamp", ThemeContext{MaxToolIterations: 99}, maxToolIterationsCeiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolIterationBudget(tc.tc); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
