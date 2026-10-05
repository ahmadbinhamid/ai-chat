package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDKnownFields = "known-fields"

// resolvePathNode resolves a dotted path against lookupRoot (the §7 model plus in-scope for-loop aliases).
// An unrecognized root returns no message — component render params (e.g. "variant") are never §7 objects.
func resolvePathNode(path string, lookupRoot func(string) (*fieldSpec, bool)) (*fieldSpec, string) {
	segments := splitPathSegments(path)
	rootName, rootSubs := splitSubscripts(segments[0])
	cur, known := lookupRoot(rootName)
	if !known {
		return nil, ""
	}
	cur, msg := applySubscripts(path, rootName, cur, rootSubs, lookupRoot)
	if msg != "" {
		return nil, msg
	}

	for i, seg := range segments[1:] {
		name, subs := splitSubscripts(seg)
		prefix := strings.Join(segments[:i+1], ".")
		if cur.children == nil {
			return nil, fmt.Sprintf(
				"'%s' treats '%s' as having sub-fields, but '%s' has none in the §7 data model.",
				path, prefix+"."+name, prefix)
		}
		next, ok := cur.children[name]
		if !ok {
			return nil, fmt.Sprintf(
				"'%s' references '%s', which is not in the §7 data model for '%s'. Only listed fields may be used — "+
					"if new data is genuinely needed, say so instead of inventing a field.",
				path, prefix+"."+name, rootName)
		}
		if cur, msg = applySubscripts(path, prefix+"."+name, next, subs, lookupRoot); msg != "" {
			return nil, msg
		}
	}
	return cur, ""
}

// applySubscripts resolves `[key]` accesses on node: an array yields its element, a childless leaf (an opaque map such as
// variants[].options) stays a leaf so no .field can follow, and an object with named fields must use dotted access.
func applySubscripts(path, at string, node *fieldSpec, subs []string, lookupRoot func(string) (*fieldSpec, bool)) (*fieldSpec, string) {
	for _, key := range subs {
		if keyPath := ParseExpression(key).Path; keyPath != "" {
			if _, msg := resolvePathNode(keyPath, lookupRoot); msg != "" {
				return nil, msg
			}
		}
		switch {
		case node.array:
			node = &fieldSpec{children: node.children}
		case node.children != nil:
			return nil, fmt.Sprintf(
				"'%s' uses '[%s]' on '%s', which has named fields in the §7 data model — use dotted access to a listed field instead.",
				path, key, at)
		}
	}
	return node, ""
}

// splitPathSegments splits path on dots outside brackets, so "a.b[c.d].e" yields "a", "b[c.d]", "e".
func splitPathSegments(path string) []string {
	var segments []string
	depth, start := 0, 0
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				segments = append(segments, path[start:i])
				start = i + 1
			}
		}
	}
	return append(segments, path[start:])
}

// splitSubscripts splits one segment like "options[choice.id]" into its name and the trimmed bodies of each top-level [...].
func splitSubscripts(seg string) (string, []string) {
	idx := strings.IndexByte(seg, '[')
	if idx < 0 {
		return seg, nil
	}
	name := seg[:idx]
	var subs []string
	depth, start := 0, 0
	for i := idx; i < len(seg); i++ {
		switch seg[i] {
		case '[':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ']':
			depth--
			if depth == 0 {
				subs = append(subs, strings.TrimSpace(seg[start:i]))
			}
		}
	}
	return name, subs
}

// checkKnownFields enforces rule 12: every object.field reference must resolve against §7, or a for-loop alias one hop deep.
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

// checkKnownFieldsInFile walks tags and outputs in document order, maintaining a stack of for-loop alias scopes.
// Scoping matters: two sequential (not nested) loops can reuse the same loop var name for different sources.
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
