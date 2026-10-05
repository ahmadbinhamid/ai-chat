package themecheck

import (
	"strings"
	"unicode"
)

const ruleIDDraftReversion = "draft-reversion"

// Fires only when a proposal drops at least 3 earlier-added lines AND half of them: a one-line edit or a targeted fix
// stays under, while a whole-file rewrite that reverts the draft trips both.
const (
	draftReversionMinDropped = 3
	draftReversionMinShare   = 0.5
)

// DraftReversion is one proposed file that drops much of what earlier pending turns added to it.
type DraftReversion struct {
	Path    string
	Dropped int // earlier-added lines missing from the proposal
	Added   int // lines earlier pending turns added on top of the saved theme
}

// DetectDraftReversions compares each proposed update of a draft file against saved (the applied theme) and draft (saved plus
// pending turns). Detection only: callers surface it as a warning, since the merchant may genuinely have asked for the undo.
func DetectDraftReversions(p Proposal, saved, draft map[string]string) []DraftReversion {
	var out []DraftReversion
	for _, f := range p.Files {
		if f.Action != "update" {
			continue
		}
		draftContent, inDraft := draft[f.Path]
		savedContent, inSaved := saved[f.Path]
		if !inDraft || !inSaved {
			continue
		}

		savedLines := normalizedLineSet(savedContent)
		proposedLines := normalizedLineSet(f.Content)
		added, dropped := 0, 0
		for line := range normalizedLineSet(draftContent) {
			if savedLines[line] {
				continue
			}
			added++
			if !proposedLines[line] {
				dropped++
			}
		}
		if dropped >= draftReversionMinDropped && float64(dropped) >= draftReversionMinShare*float64(added) {
			out = append(out, DraftReversion{Path: f.Path, Dropped: dropped, Added: added})
		}
	}
	return out
}

// Finding renders r as a merchant-readable warning; appendWarningsNote prefixes the path.
func (r DraftReversion) Finding() Finding {
	return Finding{
		Path: r.Path, Rule: ruleIDDraftReversion, Severity: SeverityWarning,
		Message: "This change may undo some of your earlier unsaved changes to this file. Check the preview before applying.",
	}
}

// normalizedLineSet collapses whitespace so re-indented or moved lines still match, and skips lines with no letter or digit
// (a lone "}" or "</div>") since they can't tell one change from another.
func normalizedLineSet(content string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		norm := strings.Join(strings.Fields(line), " ")
		if strings.IndexFunc(norm, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
			continue
		}
		set[norm] = true
	}
	return set
}
