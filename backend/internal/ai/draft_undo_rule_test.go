package ai

import (
	"strings"
	"testing"
)

func TestDynamicSystemPrompt_DraftUndoRule(t *testing.T) {
	tests := []struct {
		name  string
		draft []string
		want  bool
	}{
		{name: "non-empty draft carries the undo rule", draft: []string{"components/css/header.css"}, want: true},
		{name: "empty draft has no undo rule", draft: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dynamicSystemPrompt(ThemeContext{ThemeSlug: "shop", DraftPaths: tt.draft})
			if strings.Contains(got, draftUndoRule) != tt.want {
				t.Errorf("undo rule present = %v, want %v:\n%s", !tt.want, tt.want, got)
			}
			if tt.want && strings.Index(got, draftUndoRule) < strings.Index(got, draftPathsHeading) {
				t.Error("the undo rule belongs inside the draft-files section")
			}
		})
	}
}
