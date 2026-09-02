package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDKnownFields = "known-fields"

// resolvePathNode resolves a dotted path (e.g. "product.choices" or
// "choice.label") against lookupRoot — the §7 data model plus whatever
// for-loop aliases are in scope at this point in the file (see
// checkKnownFieldsInFile). It returns a non-empty message ONLY when the
// path's root is known but some field beneath it isn't listed for it — an
// unrecognized root produces no message at all (nil, ""): §1 says
// components receive only explicit render params, so names like `variant`
// or `background` inside a component are never §7 objects, and flagging
// every one of them would turn this into a blocking error on ordinary,
// correct code. Precision over recall (see phase 1 design notes).
func resolvePathNode(path string, lookupRoot func(string) (*fieldSpec, bool)) (*fieldSpec, string) {
	segments := strings.Split(path, ".")
	cur, known := lookupRoot(segments[0])
	if !known {
		return nil, ""
	}

	for i, seg := range segments[1:] {
		if cur.children == nil {
			return nil, fmt.Sprintf(
				"'%s' treats '%s' as having sub-fields, but '%s' has none in the §7 data model.",
				path, strings.Join(segments[:i+2], "."), strings.Join(segments[:i+1], "."))
		}
		next, ok := cur.children[seg]
		if !ok {
			return nil, fmt.Sprintf(
				"'%s' references '%s', which is not in the §7 data model for '%s'. Only listed fields may be used — "+
					"if new data is genuinely needed, say so instead of inventing a field.",
				path, strings.Join(segments[:i+2], "."), segments[0])
		}
		cur = next
	}
	return cur, ""
}

// checkKnownFields enforces rule 12: every object.field reference in a
// proposed .liquid file's output, if/elsif conditions, and for-loop sources
// must resolve against §7 (or, one hop deep, a for-loop variable bound to a
// known array field).
func checkKnownFields(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, ".liquid") {
			continue
		}
		findings = append(findings, checkKnownFieldsInFile(f.Path, f.Content)...)
	}
	return findings
}

// checkKnownFieldsInFile walks a file's tags and {{ }} outputs together in
// true document order (not as two independently-scanned lists), maintaining
// a stack of for-loop alias scopes — pushed on {% for %}, popped on
// {% endfor %}. This block-scoping matters: two sequential (not nested)
// loops in the same file reusing a short loop variable name for two
// different sources (a real pattern — e.g. `for item in menu.items` then
// later `for item in products.items`) must each resolve `item` against
// their own source, not whichever binding happened to be seen last.
func checkKnownFieldsInFile(path, content string) []Finding {
	var findings []Finding
	tags := ScanTags(content)
	outputs := ScanOutputExpressions(content)

	var scopes []map[string]*fieldSpec

	lookupRoot := func(name string) (*fieldSpec, bool) {
		if node, ok := dataModel[name]; ok {
			return node, true
		}
		for i := len(scopes) - 1; i >= 0; i-- {
			if node, ok := scopes[i][name]; ok {
				return node, true
			}
		}
		return nil, false
	}

	ti, oi := 0, 0
	for ti < len(tags) || oi < len(outputs) {
		if oi >= len(outputs) || (ti < len(tags) && tags[ti].Start <= outputs[oi].Start) {
			t := tags[ti]
			ti++

			switch t.Name {
			case "for":
				varName, source, ok := splitForTag(t.Raw)
				frame := map[string]*fieldSpec{}
				if ok {
					node, msg := resolvePathNode(source, lookupRoot)
					if msg != "" {
						findings = append(findings, knownFieldsFinding(path, t.Line, msg))
					}
					if node != nil && node.array {
						frame[varName] = &fieldSpec{children: node.children}
					}
				}
				scopes = append(scopes, frame)

			case "endfor":
				if len(scopes) > 0 {
					scopes = scopes[:len(scopes)-1]
				}

			case "if", "elsif":
				cond := parseIfCondition(t)
				for _, ref := range cond.Refs {
					if _, msg := resolvePathNode(ref, lookupRoot); msg != "" {
						findings = append(findings, knownFieldsFinding(path, t.Line, msg))
					}
				}
			}
			continue
		}

		e := outputs[oi]
		oi++
		if e.Path == "" {
			continue
		}
		if _, msg := resolvePathNode(e.Path, lookupRoot); msg != "" {
			findings = append(findings, knownFieldsFinding(path, e.Line, msg))
		}
	}

	return findings
}

func knownFieldsFinding(path string, line int, message string) Finding {
	return Finding{Path: path, Rule: ruleIDKnownFields, Severity: SeverityError, Message: fmt.Sprintf("line %d: %s", line, message), Line: line}
}
