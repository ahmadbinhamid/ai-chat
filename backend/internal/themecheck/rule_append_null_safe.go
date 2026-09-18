package themecheck

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleIDAppendNullSafe = "append-null-safe"

// appendArgRe finds `| append: <ident>` where the argument is a bare variable
// (not a quoted string). Keepsuit TypeErrors when that ident is nil — and
// `nil == blank` is FALSE in this dialect, so a prior blank-check is not enough.
var appendArgRe = regexp.MustCompile(`(?i)\|\s*append\s*:\s*([a-zA-Z_][\w.]*)`)

// assignDefaultRe matches `{% assign <name> = … | default: … %}` (any filters
// before default still count — we only need default somewhere in the RHS).
var assignDefaultRe = regexp.MustCompile(`(?is)\{\%-?\s*assign\s+([a-zA-Z_][\w.]*)\s*=[^%]*\|\s*default\s*:`)

// checkAppendNullSafe rejects `| append: var` when `var` was not assigned with
// `| default:` earlier in the same file. Prevents InternalException on /shop
// style crashes from missing settings nested fields.
func checkAppendNullSafe(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		defaulted := map[string]bool{}
		for _, m := range assignDefaultRe.FindAllStringSubmatch(f.Content, -1) {
			defaulted[m[1]] = true
		}
		for _, m := range appendArgRe.FindAllStringSubmatchIndex(f.Content, -1) {
			name := f.Content[m[2]:m[3]]
			if defaulted[name] {
				continue
			}
			// Literal-looking dotted paths that are string constants never appear
			// as append targets in practice; still require default on the leaf assign.
			line := lineAt(f.Content, m[0])
			findings = append(findings, Finding{
				Rule:     ruleIDAppendNullSafe,
				Severity: SeverityError,
				Path:     f.Path,
				Line:     line,
				Message: fmt.Sprintf(
					"line %d: '| append: %s' crashes this Liquid engine when %s is nil "+
						"(nil == blank is FALSE here, unlike Shopify). Assign with "+
						"'| default: \"…\"' first, e.g. '{%% assign %s = … | default: \"\" %%}'.",
					line, name, name, name),
			})
		}
	}
	return findings
}
