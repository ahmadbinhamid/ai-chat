package ai

import (
	"strings"
	"testing"
)

const draftPathsHeading = "Files with unsaved changes from earlier turns"

func TestDynamicSystemPrompt_DraftPaths(t *testing.T) {
	base := ThemeContext{ThemeSlug: "shop", PagesJSON: `[{"slug":"home"}]`, DefaultsJSON: `{}`}
	withDraft := base
	withDraft.DraftPaths = []string{"pages/home.liquid", "components/css/header.css", "js/minicart.js"}

	tests := []struct {
		name string
		tc   ThemeContext
		want string // "" means the section must be absent
	}{
		{"empty draft omits the section", base, ""},
		{"non-empty draft lists paths sorted", withDraft,
			"  - components/css/header.css\n  - js/minicart.js\n  - pages/home.liquid\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dynamicSystemPrompt(tt.tc)
			if tt.want == "" {
				if strings.Contains(got, draftPathsHeading) {
					t.Errorf("expected no draft section, got:\n%s", got)
				}
				return
			}
			idx := strings.Index(got, draftPathsHeading)
			if idx < 0 {
				t.Fatalf("expected a draft section, got:\n%s", got)
			}
			if !strings.HasSuffix(got, tt.want) {
				t.Errorf("expected the sorted list at the very end of the dynamic block, got:\n%s", got[idx:])
			}
		})
	}
}

// The draft section is appended, so everything before it — the cached part of the dynamic block — is unchanged.
func TestDynamicSystemPrompt_DraftSectionOnlyAppends(t *testing.T) {
	base := ThemeContext{ThemeSlug: "shop"}
	withDraft := base
	withDraft.DraftPaths = []string{"pages/home.liquid"}
	if !strings.HasPrefix(dynamicSystemPrompt(withDraft), dynamicSystemPrompt(base)) {
		t.Error("expected the no-draft dynamic prompt to be a prefix of the with-draft one")
	}
}

func TestDynamicSystemPrompt_DraftOrderIndependent(t *testing.T) {
	a := ThemeContext{ThemeSlug: "shop", DraftPaths: []string{"b.css", "a.liquid"}}
	b := ThemeContext{ThemeSlug: "shop", DraftPaths: []string{"a.liquid", "b.css"}}
	if dynamicSystemPrompt(a) != dynamicSystemPrompt(b) {
		t.Error("expected identical prompts for the same draft paths in a different order")
	}
	if a.DraftPaths[0] != "b.css" {
		t.Error("formatDraftPaths must not reorder the caller's slice")
	}
}

// staticSystemPromptBlock takes no ThemeContext, so a draft can never reach the cached static block.
func TestStaticSystemPrompt_UnaffectedByDraft(t *testing.T) {
	before := staticSystemPromptBlock().Text
	_ = dynamicSystemPrompt(ThemeContext{DraftPaths: []string{"pages/home.liquid"}})
	if staticSystemPromptBlock().Text != before {
		t.Error("static system prompt changed")
	}
	if strings.Contains(before, draftPathsHeading) {
		t.Error("draft section leaked into the static block")
	}
}
