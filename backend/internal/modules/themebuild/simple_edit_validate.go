package themebuild

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"ai-chat/internal/ai"
)

// SIMPLE_EDIT output / patch budgets — intentionally below the generic
// interactive 16k ceiling so a one-shot call cannot regenerate huge files.
const (
	simpleEditMaxTokens         int64 = 8_000
	simpleEditMaxUpdateChars          = 6_000 // full-file "update" over this is rejected
	simpleEditMaxFiles                = 3
)

// validateSimpleEditCompactness rejects proposals that are too broad for the
// simple-edit path. Note: after MaterializeEdits, action "edit" becomes
// "update" with full file content — so we do NOT reject update size here.
// Full-file "update" bloating is rejected pre-materialize in package ai
// when SimpleEditOneShot is set.
func validateSimpleEditCompactness(result *ai.Result) error {
	if result == nil {
		return fmt.Errorf("%w: empty proposal", ai.ErrSimpleEditBudget)
	}
	if len(result.Files) == 0 {
		return nil // clarification / question — ok
	}
	if len(result.Files) > simpleEditMaxFiles {
		return fmt.Errorf("%w: too many files in changeset (%d > %d)",
			ai.ErrSimpleEditBudget, len(result.Files), simpleEditMaxFiles)
	}
	for _, f := range result.Files {
		if strings.EqualFold(f.Action, "create") && utf8.RuneCountInString(f.Content) > simpleEditMaxUpdateChars {
			return fmt.Errorf("%w: create for %s is too large (%d chars)",
				ai.ErrSimpleEditBudget, f.Path, utf8.RuneCountInString(f.Content))
		}
	}
	return nil
}

func simpleEditPatchSize(result *ai.Result) int {
	if result == nil {
		return 0
	}
	n := 0
	for _, f := range result.Files {
		n += utf8.RuneCountInString(f.Content)
		for _, e := range f.Edits {
			n += utf8.RuneCountInString(e.OldString) + utf8.RuneCountInString(e.NewString)
		}
	}
	return n
}
