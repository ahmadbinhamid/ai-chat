// Package liquidrender executes theme_engine_spec.md's simplified Liquid
// dialect against real data — the tag/filter subset in §1 (render,
// if/elsif/else/endif, for/endfor with forloop.first/last, assign, capture,
// comment, and the 7 filters), nothing more. It exists for phase 5's render
// preview: a merchant's dashboard needs to see a page rendered with
// realistic data (see themefs.FixtureContext) before ever writing it to the
// real theme, and no general-purpose Shopify-Liquid library would enforce
// (or even necessarily support) this bespoke, narrower dialect's exact
// rules — e.g. render's path-prefix convention, or the boolean-ish
// true/false/1/0 coercion §1 requires. A generation's own correctness is
// themecheck's job (static analysis); this package actually runs the
// template.
package liquidrender

import (
	"regexp"
	"strings"
)

type tokenKind int

const (
	tokenText tokenKind = iota
	tokenTag
	tokenOutput
)

// token is one lexical unit of a template: literal text, a {% tag %}, or a
// {{ output }} expression.
type token struct {
	kind tokenKind
	text string // tokenText: the literal text; tokenTag: the tag name
	raw  string // tokenTag: everything after the name; tokenOutput: the expression
	line int
}

var tagOrOutputRe = regexp.MustCompile(`(?s)\{%-?\s*(\w+)(.*?)-?%\}|\{\{-?\s*(.*?)\s*-?\}\}`)

// tokenize splits src into an ordered stream of text/tag/output tokens.
func tokenize(src string) []token {
	matches := tagOrOutputRe.FindAllStringSubmatchIndex(src, -1)
	toks := make([]token, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		if m[0] > last {
			toks = append(toks, token{kind: tokenText, text: src[last:m[0]], line: lineAt(src, last)})
		}
		if m[2] != -1 {
			name := src[m[2]:m[3]]
			raw := strings.TrimSpace(src[m[4]:m[5]])
			toks = append(toks, token{kind: tokenTag, text: name, raw: raw, line: lineAt(src, m[0])})
		} else {
			raw := src[m[6]:m[7]]
			toks = append(toks, token{kind: tokenOutput, raw: raw, line: lineAt(src, m[0])})
		}
		last = m[1]
	}
	if last < len(src) {
		toks = append(toks, token{kind: tokenText, text: src[last:], line: lineAt(src, last)})
	}
	return toks
}

func lineAt(s string, offset int) int {
	if offset > len(s) {
		offset = len(s)
	}
	return 1 + strings.Count(s[:offset], "\n")
}

// splitTopLevel splits s on sep, ignoring sep inside single/double-quoted
// substrings.
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

// splitOnWord splits s on whole-word occurrences of word, ignoring quoted
// substrings — used for the "or" in an if condition.
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

func leftBoundary(s string, i int) bool { return i == 0 || !isWordChar(s[i-1]) }
func rightBoundary(s string, j int) bool {
	return j == len(s) || !isWordChar(s[j])
}
func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// splitRenderArgs splits a render tag's raw body ("'target', key: value,
// ...") into its quoted target and ordered key:value pairs.
func splitRenderArgs(raw string) (target string, params []renderParam, ok bool) {
	args := splitTopLevel(raw, ',')
	if len(args) == 0 {
		return "", nil, false
	}
	first := strings.TrimSpace(args[0])
	if len(first) < 2 {
		return "", nil, false
	}
	quote := first[0]
	if quote != '\'' && quote != '"' || first[len(first)-1] != quote {
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
		params = append(params, renderParam{
			key:   strings.TrimSpace(arg[:idx]),
			value: strings.TrimSpace(arg[idx+1:]),
		})
	}
	return target, params, true
}

type renderParam struct {
	key   string
	value string
}
