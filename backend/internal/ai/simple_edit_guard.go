package ai

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Simple-edit pre-materialize guards — reject full-file regenerations before
// they become draft "update" payloads. Truncated streams are already rejected
// via errMaxTokensTruncated (StopReasonMaxTokens) and never reach here.
const (
	simpleEditMaxFullUpdateRunes = 6_000
	simpleEditMaxEditPairRunes   = 4_000 // sum of old+new per file
	simpleEditMaxFilesInPropose  = 3
)

// rejectBloatedSimpleEditProposal inspects the raw propose_changes Result
// (before MaterializeEdits expands "edit" into full-file "update").
func rejectBloatedSimpleEditProposal(result *Result) error {
	if result == nil {
		return nil
	}
	if len(result.Files) > simpleEditMaxFilesInPropose {
		return fmt.Errorf("simple_edit: too many files (%d) — keep the changeset to %d files or fewer",
			len(result.Files), simpleEditMaxFilesInPropose)
	}
	for _, f := range result.Files {
		action := strings.ToLower(f.Action)
		switch action {
		case "update":
			n := utf8.RuneCountInString(f.Content)
			if n > simpleEditMaxFullUpdateRunes {
				return fmt.Errorf("simple_edit: full-file update for %s is too large (%d chars) — use action \"edit\" with small old_string/new_string patches instead",
					f.Path, n)
			}
		case "edit":
			sum := 0
			for _, e := range f.Edits {
				sum += utf8.RuneCountInString(e.OldString) + utf8.RuneCountInString(e.NewString)
			}
			if len(f.Edits) == 0 {
				return fmt.Errorf("simple_edit: %s action \"edit\" requires at least one edits[] pair", f.Path)
			}
			if sum > simpleEditMaxEditPairRunes {
				return fmt.Errorf("simple_edit: edit patches for %s are too large (%d chars) — make a smaller change",
					f.Path, sum)
			}
		case "create":
			if utf8.RuneCountInString(f.Content) > simpleEditMaxFullUpdateRunes {
				return fmt.Errorf("simple_edit: create for %s is too large for a simple edit", f.Path)
			}
		}
	}
	return nil
}
