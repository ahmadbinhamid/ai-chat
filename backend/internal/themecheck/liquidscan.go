package themecheck

import (
	"regexp"
	"strings"
)

// Tag is one {% ... %} occurrence in a Liquid source, in source order.
// Whitespace-trim markers ({%- / -%}) are stripped before Name/Raw are
// extracted, so callers never have to account for them separately.
type Tag struct {
	Name  string // e.g. "if", "endif", "for", "render", "assign", "capture", "comment"
	Raw   string // trimmed tag body after the name, e.g. "x == true or x == 1"
	Line  int    // 1-based line the tag starts on
	Start int    // byte offset of the tag's opening "{%" — lets callers interleave tags with output expressions in true document order
}

var tagRe = regexp.MustCompile(`(?s)\{%-?\s*(\w+)(.*?)-?%\}`)

// ScanTags returns every {% ... %} tag found in content, in source order.
func ScanTags(content string) []Tag {
	matches := tagRe.FindAllStringSubmatchIndex(content, -1)
	tags := make([]Tag, 0, len(matches))
	for _, m := range matches {
		name := content[m[2]:m[3]]
		raw := strings.TrimSpace(content[m[4]:m[5]])
		tags = append(tags, Tag{Name: name, Raw: raw, Line: lineAt(content, m[0]), Start: m[0]})
	}
	return tags
}

// Expression is one parsed Liquid value expression — the body of a {{ }}
// output, or one render/assign param's value.
type Expression struct {
	Raw     string   // trimmed original expression text
	Path    string   // dotted identifier path, "" if this is a literal or keyword
	Filters []string // filter names in order (the part after each top-level `|`)
	Line    int
	Start   int // byte offset of the {{ — lets callers interleave with tags in document order
}

var outputRe = regexp.MustCompile(`(?s)\{\{-?\s*(.*?)\s*-?\}\}`)

// ScanOutputExpressions returns every {{ ... }} output expression in
// content, parsed.
func ScanOutputExpressions(content string) []Expression {
	matches := outputRe.FindAllStringSubmatchIndex(content, -1)
	exprs := make([]Expression, 0, len(matches))
	for _, m := range matches {
		raw := content[m[2]:m[3]]
		expr := ParseExpression(raw)
		expr.Line = lineAt(content, m[0])
		expr.Start = m[0]
		exprs = append(exprs, expr)
	}
	return exprs
}

// literalKeywords are bare words that are valid Liquid expression values but
// are not §7 data-model references.
var literalKeywords = map[string]bool{
	"true": true, "false": true, "nil": true, "null": true,
	"blank": true, "empty": true,
}

var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// ParseExpression parses a single Liquid value expression into its dotted
// identifier path (empty if it's a quoted string/number/keyword literal)
// and any `| filter: args` pipeline applied to it.
func ParseExpression(raw string) Expression {
	raw = strings.TrimSpace(raw)
	parts := splitTopLevel(raw, '|')
	base := strings.TrimSpace(parts[0])

	expr := Expression{Raw: raw}
	switch {
	case base == "":
	case strings.HasPrefix(base, "'") || strings.HasPrefix(base, "\""):
		// string literal
	case numberRe.MatchString(base):
		// number literal
	case literalKeywords[base]:
		// keyword literal
	default:
		expr.Path = base
	}

	for _, f := range parts[1:] {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		name := f
		if idx := strings.IndexByte(f, ':'); idx >= 0 {
			name = f[:idx]
		}
		expr.Filters = append(expr.Filters, strings.TrimSpace(name))
	}
	return expr
}

// splitTopLevel splits s on sep, ignoring occurrences of sep inside single-
// or double-quoted substrings — so a filter argument like `'a, b' `containing
// the separator doesn't produce a spurious split.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// RenderParam is one key: value argument of a {% render %} tag, in source
// order.
type RenderParam struct {
	Key   string
	Value string // raw, trimmed expression text — parse with ParseExpression if needed
}

// ParseRenderTag parses a render tag's Raw body (as produced by ScanTags) —
// e.g. `'liquid/layout-start', page: page, store: store` — into its target
// path (quotes stripped) and ordered key:value params. ok is false if the
// first argument isn't a quoted literal, which every render call's target
// must be per spec §1.
func ParseRenderTag(raw string) (target string, params []RenderParam, ok bool) {
	args := splitTopLevel(raw, ',')
	if len(args) == 0 {
		return "", nil, false
	}
	first := strings.TrimSpace(args[0])
	if len(first) < 2 {
		return "", nil, false
	}
	quote := first[0]
	if quote != '\'' && quote != '"' {
		return "", nil, false
	}
	if first[len(first)-1] != quote {
		return "", nil, false
	}
	target = first[1 : len(first)-1]

	for _, arg := range args[1:] {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		idx := strings.IndexByte(arg, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(arg[:idx])
		value := strings.TrimSpace(arg[idx+1:])
		params = append(params, RenderParam{Key: key, Value: value})
	}
	return target, params, true
}

// splitForTag splits a {% for %} tag's Raw body ("choice in product.choices")
// into its loop variable and source path. ok is false if Raw isn't a
// recognizable "x in y" form.
func splitForTag(raw string) (varName, source string, ok bool) {
	idx := strings.Index(raw, " in ")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+len(" in "):]), true
}

// IfCondition is one {% if %} / {% elsif %} tag's condition, parsed.
// Intentionally shallow: this dialect's conditions are always either a bare
// identifier (`x`), an `x != blank`-style comparison, or the required
// bool-ish guard `x == true or x == 1` (§1) — never boolean algebra beyond
// that.
type IfCondition struct {
	Raw  string
	Line int
	// Refs is every distinct identifier path tested in this condition.
	Refs []string
	// Bare is true when the condition is just a dotted path with no
	// operator at all (`{% if x %}`).
	Bare bool
	// GuardsBoolIsh is true when the condition is exactly `x == true or x
	// == 1` for a single path x, in either operand order.
	GuardsBoolIsh bool
}

var comparisonRe = regexp.MustCompile(`^(\S+)\s*(==|!=)\s*(\S+)$`)

// ScanIfConditions returns every {% if %} / {% elsif %} tag in content,
// parsed.
func ScanIfConditions(content string) []IfCondition {
	var conds []IfCondition
	for _, t := range ScanTags(content) {
		if t.Name != "if" && t.Name != "elsif" {
			continue
		}
		conds = append(conds, parseIfCondition(t))
	}
	return conds
}

func parseIfCondition(t Tag) IfCondition {
	cond := IfCondition{Raw: t.Raw, Line: t.Line}
	pieces := splitOnWord(t.Raw, "or")

	type comparison struct {
		path, op, val string
	}
	var comparisons []comparison
	seen := map[string]bool{}
	addRef := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			cond.Refs = append(cond.Refs, path)
		}
	}
	for _, p := range pieces {
		p = strings.TrimSpace(p)
		if m := comparisonRe.FindStringSubmatch(p); m != nil {
			comparisons = append(comparisons, comparison{path: m[1], op: m[2], val: m[3]})
			addRef(m[1])
		} else {
			addRef(p)
		}
	}

	// No piece parsed as a comparison at all: this is a bare truthy check
	// (`{% if x %}`, or `{% if x or y %}`), never a guard.
	cond.Bare = len(comparisons) == 0

	if len(comparisons) == 2 && comparisons[0].path == comparisons[1].path {
		vals := map[string]bool{comparisons[0].val: true, comparisons[1].val: true}
		bothEq := comparisons[0].op == "==" && comparisons[1].op == "=="
		if bothEq && vals["true"] && vals["1"] {
			cond.GuardsBoolIsh = true
		}
	}
	return cond
}

// splitOnWord splits s on whole-word occurrences of word (e.g. "or"),
// leaving quoted substrings untouched.
func splitOnWord(s, word string) []string {
	var parts []string
	var quote byte
	start := 0
	i := 0
	for i < len(s) {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			i++
			continue
		}
		if leftBoundary(s, i) && strings.HasPrefix(s[i:], word) && rightBoundary(s, i+len(word)) {
			parts = append(parts, s[start:i])
			start = i + len(word)
			i = start
			continue
		}
		i++
	}
	parts = append(parts, s[start:])
	return parts
}

// leftBoundary reports whether position i is a valid start for a whole-word
// match — i.e. it's the start of s, or the preceding character isn't a word
// character.
func leftBoundary(s string, i int) bool {
	return i == 0 || !isWordChar(s[i-1])
}

// rightBoundary reports whether position j is a valid end for a whole-word
// match — i.e. it's the end of s, or the following character isn't a word
// character.
func rightBoundary(s string, j int) bool {
	return j == len(s) || !isWordChar(s[j])
}

func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func lineAt(content string, offset int) int {
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(content[:offset], "\n")
}
