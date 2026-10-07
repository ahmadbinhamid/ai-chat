package pageintent

import "regexp"

// undoRe is deliberately over-inclusive: a false match only keeps the draft-reversion check warning-only, as it was
// before it could block, while a miss would send a real undo request back to repair.
var undoRe = regexp.MustCompile(`(?i)\b(?:undo|undid|revert(?:ed|ing)?|roll ?back|go(?:ing)? back to|restore[ds]?|restoring|` +
	`(?:change|changes|put|set|switch|turn) (?:it|them|that|this|those|everything|all|the \w+) back|` +
	`back to (?:how|what|the way) it was|` +
	`(?:remove|delete|drop|discard|get rid of|take out) (?:the |my |your |those |these |all )*(?:earlier |previous |last |recent )?(?:changes|edits)|` +
	`the original|the previous version|previous version|as it was before)\b`)

// DetectUndo reports whether prompt asks to undo, revert or restore earlier changes.
func DetectUndo(prompt string) bool {
	return undoRe.MatchString(prompt)
}
