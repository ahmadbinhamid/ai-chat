package ai

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Simple-edit pre-materialize guards — reject full-file regenerations before
// they become draft "update" payloads. Truncated streams are already rejected
// via errMaxTokensTruncated (StopReasonMaxTokens) and never reach here.
// Callers (themebuild) must escalate to complex_page on these errors — never
// surface them as a dead-end "try a smaller request" to the merchant.
const (
	simpleEditMaxFullUpdateRunes = 6_000
	simpleEditMaxEditPairRunes   = 4_000 // sum of old+new per file
	simpleEditMaxFilesInPropose  = 3
)

// ErrSimpleEditBudget means the one-shot simple_edit path cannot accept this
// proposal (too many files / patches / full-file body). Safe to escalate.
var ErrSimpleEditBudget = errors.New("simple_edit budget exceeded")

// IsSimpleEditBudgetError reports whether err is a simple_edit size/scope
// rejection (including wrapped ErrSimpleEditBudget).
func IsSimpleEditBudgetError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSimpleEditBudget) {
		return true
	}
	return strings.Contains(err.Error(), "simple_edit:")
}

// rejectBloatedSimpleEditProposal inspects the raw propose_changes Result
// (before MaterializeEdits expands "edit" into full-file "update").
func rejectBloatedSimpleEditProposal(result *Result) error {
	if result == nil {
		return nil
	}
	if len(result.Files) > simpleEditMaxFilesInPropose {
		return fmt.Errorf("%w: too many files (%d) — keep the changeset to %d files or fewer",
			ErrSimpleEditBudget, len(result.Files), simpleEditMaxFilesInPropose)
	}
	for _, f := range result.Files {
		action := strings.ToLower(f.Action)
		switch action {
		case "update":
			n := utf8.RuneCountInString(f.Content)
			if n > simpleEditMaxFullUpdateRunes {
				return fmt.Errorf("%w: full-file update for %s is too large (%d chars)",
					ErrSimpleEditBudget, f.Path, n)
			}
		case "edit":
			sum := 0
			for _, e := range f.Edits {
				sum += utf8.RuneCountInString(e.OldString) + utf8.RuneCountInString(e.NewString)
			}
			if len(f.Edits) == 0 {
				return fmt.Errorf("%w: %s action \"edit\" requires at least one edits[] pair",
					ErrSimpleEditBudget, f.Path)
			}
			if sum > simpleEditMaxEditPairRunes {
				return fmt.Errorf("%w: edit patches for %s are too large (%d chars)",
					ErrSimpleEditBudget, f.Path, sum)
			}
		case "create":
			if utf8.RuneCountInString(f.Content) > simpleEditMaxFullUpdateRunes {
				return fmt.Errorf("%w: create for %s is too large for a simple edit",
					ErrSimpleEditBudget, f.Path)
			}
		}
	}
	return nil
}
