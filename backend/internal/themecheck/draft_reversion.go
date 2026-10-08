package themecheck

import (
	"fmt"
	"strings"
	"unicode"
)

const ruleIDDraftReversion = "draft-reversion"

// RuleDraftReversion lets callers tell a blocked reversion apart from other findings.
const RuleDraftReversion = ruleIDDraftReversion

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

		added, dropped := droppedDraftLines(savedContent, draftContent, f.Content)
		if len(dropped) >= draftReversionMinDropped && float64(len(dropped)) >= draftReversionMinShare*float64(added) {
			out = append(out, DraftReversion{Path: f.Path, Dropped: len(dropped), Added: added})
		}
	}
	return out
}

// draftReversionQuotedLines caps how many dropped lines a blocking finding quotes: enough to show what to restore
// without the repair prompt carrying a whole file.
const (
	draftReversionQuotedLines   = 5
	draftReversionQuotedLineMax = 200
)

// DraftReversionFindings is DetectDraftReversions as blocking findings for the repair round, each quoting the first
// dropped lines in file order. Callers skip it when the merchant asked for the undo.
func DraftReversionFindings(p Proposal, saved, draft map[string]string) []Finding {
	reversions := DetectDraftReversions(p, saved, draft)
	if len(reversions) == 0 {
		return nil
	}
	proposed := make(map[string]string, len(p.Files))
	for _, f := range p.Files {
		proposed[f.Path] = f.Content
	}
	findings := make([]Finding, 0, len(reversions))
	for _, r := range reversions {
		_, dropped := droppedDraftLines(saved[r.Path], draft[r.Path], proposed[r.Path])
		if len(dropped) > draftReversionQuotedLines {
			dropped = dropped[:draftReversionQuotedLines]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Your change to `%s` removes earlier unsaved work the merchant hasn't asked to undo. "+
			"Keep those lines and change only what this request needs:", r.Path)
		for _, line := range dropped {
			if len(line) > draftReversionQuotedLineMax {
				line = line[:draftReversionQuotedLineMax] + "…"
			}
			fmt.Fprintf(&b, "\n    %s", line)
		}
		findings = append(findings, Finding{Path: r.Path, Rule: ruleIDDraftReversion, Severity: SeverityError, Message: b.String()})
	}
	return findings
}

// droppedDraftLines returns how many distinct lines the draft added on top of saved, and those of them missing from
// proposed, in draft order with each original indentation trimmed.
func droppedDraftLines(saved, draft, proposed string) (added int, dropped []string) {
	savedLines := normalizedLineSet(saved)
	proposedLines := normalizedLineSet(proposed)
	seen := make(map[string]bool)
	for _, line := range strings.Split(draft, "\n") {
		norm := normalizeLine(line)
		if norm == "" || savedLines[norm] || seen[norm] {
			continue
		}
		seen[norm] = true
		added++
		if !proposedLines[norm] {
			dropped = append(dropped, strings.TrimSpace(line))
		}
	}
	return added, dropped
}

// Finding renders r as a merchant-readable warning; appendWarningsNote prefixes the path.
func (r DraftReversion) Finding() Finding {
	return Finding{
		Path: r.Path, Rule: ruleIDDraftReversion, Severity: SeverityWarning,
		Message: "This change may undo some of your earlier unsaved changes to this file. Check the preview before applying.",
	}
}

// ReplacedFinding renders r for a redesign that was allowed to replace earlier unsaved work: never silently.
func (r DraftReversion) ReplacedFinding() Finding {
	return Finding{
		Path: r.Path, Rule: ruleIDDraftReversion, Severity: SeverityWarning,
		Message: "Your earlier unsaved changes to this file were replaced by this redesign. Use Undo on this message to get them back.",
	}
}

// normalizedLineSet collapses whitespace so re-indented or moved lines still match, and skips lines with no letter or digit
// (a lone "}" or "</div>") since they can't tell one change from another.
func normalizedLineSet(content string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		if norm := normalizeLine(line); norm != "" {
			set[norm] = true
		}
	}
	return set
}

// normalizeLine is normalizedLineSet's per-line rule; "" for a line it skips.
func normalizeLine(line string) string {
	norm := strings.Join(strings.Fields(line), " ")
	if strings.IndexFunc(norm, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
		return ""
	}
	return norm
}
