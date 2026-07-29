package liquidrender

import (
	"fmt"
	"strings"
)

// maxRenderDepth guards against a render cycle (a component rendering
// itself, directly or via a chain) — a real error case for a preview, not
// something to hang forever on.
const maxRenderDepth = 20

// Renderer executes templates from Files (theme-relative path, including
// its .liquid extension -> content) against a starting variable scope.
type Renderer struct {
	Files map[string]string
}

// Render renders entryPath (e.g. "pages/offers.liquid") with vars as its
// top-level scope, returning the produced HTML and any errors encountered
// (an unsupported tag/filter, a missing render target, etc.) — errors don't
// stop rendering; the rest of the template still produces output around
// the failure, which is more useful for a preview than an all-or-nothing
// failure.
func (r *Renderer) Render(entryPath string, vars map[string]any) (string, []string) {
	var errs []string
	out := r.renderFile(entryPath, scope(vars), &errs, 0)
	return out, errs
}

func (r *Renderer) renderFile(path string, vars scope, errs *[]string, depth int) string {
	if depth > maxRenderDepth {
		*errs = append(*errs, fmt.Sprintf("render depth exceeded at %q (possible render cycle)", path))
		return ""
	}
	content, ok := r.Files[path]
	if !ok {
		*errs = append(*errs, fmt.Sprintf("render target not found: %q", path))
		return ""
	}
	toks := tokenize(content)
	var b strings.Builder
	pos := 0
	r.execBlock(toks, &pos, vars, &b, errs, depth, nil)
	return b.String()
}

// execBlock renders tokens from *pos into b until EOF or a tag whose name
// is in stopAt, returning that tag's name ("" at EOF) without consuming it
// — the caller (execIf, capture) decides what to do next.
func (r *Renderer) execBlock(toks []token, pos *int, vars scope, b *strings.Builder, errs *[]string, depth int, stopAt map[string]bool) string {
	for *pos < len(toks) {
		t := toks[*pos]

		if t.kind == tokenTag && stopAt != nil && stopAt[t.text] {
			return t.text
		}

		switch t.kind {
		case tokenText:
			b.WriteString(t.text)
			*pos++

		case tokenOutput:
			b.WriteString(toDisplayString(eval(t.raw, vars, errs)))
			*pos++

		case tokenTag:
			switch t.text {
			case "comment":
				*pos++
				skipBlock(toks, pos, map[string]bool{"endcomment": true})
				if *pos < len(toks) {
					*pos++ // consume endcomment
				}
			case "if":
				*pos++
				r.execIf(toks, pos, t.raw, vars, b, errs, depth)
			case "for":
				*pos++
				r.execFor(toks, pos, t.raw, vars, b, errs, depth)
			case "assign":
				*pos++
				execAssign(t.raw, vars, errs)
			case "capture":
				name := strings.TrimSpace(t.raw)
				*pos++
				var cb strings.Builder
				r.execBlock(toks, pos, vars, &cb, errs, depth, map[string]bool{"endcapture": true})
				if *pos < len(toks) {
					*pos++ // consume endcapture
				}
				vars[name] = cb.String()
			case "render":
				*pos++
				r.execRender(t.raw, vars, b, errs, depth)
			default:
				*errs = append(*errs, fmt.Sprintf("line %d: unsupported tag %q", t.line, t.text))
				*pos++
			}
		}
	}
	return ""
}

// execIf handles a full if/elsif*/else?/endif chain: at most one branch's
// body is rendered, per standard if-chain semantics.
func (r *Renderer) execIf(toks []token, pos *int, cond string, vars scope, b *strings.Builder, errs *[]string, depth int) {
	matched := evalCondition(cond, vars, errs)

	for {
		if matched {
			stop := r.execBlock(toks, pos, vars, b, errs, depth, map[string]bool{"elsif": true, "else": true, "endif": true})
			// Skip every remaining branch unrendered.
			for stop == "elsif" || stop == "else" {
				*pos++ // consume the branch tag we stopped at
				if stop == "elsif" {
					skipCondTag(toks, pos)
				}
				stop = skipBlock(toks, pos, map[string]bool{"elsif": true, "else": true, "endif": true})
			}
			if *pos < len(toks) {
				*pos++ // consume endif
			}
			return
		}

		// This branch didn't match — skip its body without rendering.
		stop := skipBlock(toks, pos, map[string]bool{"elsif": true, "else": true, "endif": true})
		if stop == "endif" {
			*pos++
			return
		}
		if stop == "" {
			return // malformed template, EOF reached
		}
		// stop is "elsif" or "else": consume it and evaluate/render next.
		branchRaw := toks[*pos].raw
		isElse := toks[*pos].text == "else"
		*pos++
		if isElse {
			matched = true
		} else {
			matched = evalCondition(branchRaw, vars, errs)
		}
	}
}

// skipCondTag is a no-op placeholder kept for symmetry with execIf's
// branch-consuming logic — elsif's condition is read directly from the
// token when needed, nothing to separately skip here.
func skipCondTag(_ []token, _ *int) {}

// execFor handles "{% for x in y %}...{% endfor %}", binding x plus
// forloop.first/forloop.last (§1 — no other forloop field is supported)
// for each element of y, which must evaluate to a slice.
func (r *Renderer) execFor(toks []token, pos *int, raw string, vars scope, b *strings.Builder, errs *[]string, depth int) {
	bodyStart := *pos

	idx := strings.Index(raw, " in ")
	if idx < 0 {
		*errs = append(*errs, fmt.Sprintf("malformed for tag: %q", raw))
		skipBlock(toks, pos, map[string]bool{"endfor": true})
		if *pos < len(toks) {
			*pos++
		}
		return
	}
	varName := strings.TrimSpace(raw[:idx])
	source := eval(strings.TrimSpace(raw[idx+len(" in "):]), vars, errs)

	items, ok := source.([]any)
	if !ok {
		if source != nil {
			*errs = append(*errs, fmt.Sprintf("for %s: source is not a list", varName))
		}
		skipBlock(toks, pos, map[string]bool{"endfor": true})
		if *pos < len(toks) {
			*pos++
		}
		return
	}

	if len(items) == 0 {
		skipBlock(toks, pos, map[string]bool{"endfor": true})
		if *pos < len(toks) {
			*pos++
		}
		return
	}

	for i, item := range items {
		vars[varName] = item
		vars["forloop"] = map[string]any{"first": i == 0, "last": i == len(items)-1}
		*pos = bodyStart
		r.execBlock(toks, pos, vars, b, errs, depth, map[string]bool{"endfor": true})
	}
	if *pos < len(toks) {
		*pos++ // consume endfor
	}
	delete(vars, "forloop")
}

// execAssign handles "{% assign x = <expr> %}".
func execAssign(raw string, vars scope, errs *[]string) {
	idx := strings.IndexByte(raw, '=')
	if idx < 0 {
		*errs = append(*errs, fmt.Sprintf("malformed assign tag: %q", raw))
		return
	}
	name := strings.TrimSpace(raw[:idx])
	vars[name] = eval(strings.TrimSpace(raw[idx+1:]), vars, errs)
}

// execRender handles "{% render 'target', key: value, ... %}" — only the
// explicit params are visible inside the partial (§1: "never rely on
// implicit/global scope leaking into a component").
func (r *Renderer) execRender(raw string, vars scope, b *strings.Builder, errs *[]string, depth int) {
	target, params, ok := splitRenderArgs(raw)
	if !ok {
		*errs = append(*errs, fmt.Sprintf("malformed render tag: %q", raw))
		return
	}
	if !strings.HasPrefix(target, "liquid/") && !strings.HasPrefix(target, "components/") {
		*errs = append(*errs, fmt.Sprintf("render path %q must start with 'liquid/' or 'components/'", target))
		return
	}

	partialVars := scope{}
	for _, p := range params {
		partialVars[p.key] = evalValue(p.value, vars)
	}

	b.WriteString(r.renderFile(target+".liquid", partialVars, errs, depth+1))
}

// skipBlock advances *pos past tokens until it finds one of stopNames at
// the same nesting level — an inner if/for/capture's own matching close
// doesn't count, so nested blocks inside a skipped branch don't confuse
// the search for the OUTER boundary.
func skipBlock(toks []token, pos *int, stopNames map[string]bool) string {
	depth := 0
	for *pos < len(toks) {
		t := toks[*pos]
		if t.kind == tokenTag {
			switch t.text {
			case "if", "for", "capture":
				depth++
			case "endif", "endfor", "endcapture":
				if depth > 0 {
					depth--
					*pos++
					continue
				}
			case "elsif", "else":
				if depth > 0 {
					*pos++
					continue
				}
			}
			if depth == 0 && stopNames[t.text] {
				return t.text
			}
		}
		*pos++
	}
	return ""
}

// evalCondition evaluates an if/elsif condition: a bare value, an
// "A == B"/"A != B" comparison, or several such comparisons joined by
// "or" — the only forms §1's dialect uses.
func evalCondition(raw string, vars scope, errs *[]string) bool {
	for _, part := range splitOnWord(raw, "or") {
		part = strings.TrimSpace(part)
		if idx := findOperator(part, "=="); idx >= 0 {
			left := eval(part[:idx], vars, errs)
			right := eval(part[idx+2:], vars, errs)
			if looseEqual(left, right) {
				return true
			}
			continue
		}
		if idx := findOperator(part, "!="); idx >= 0 {
			left := eval(part[:idx], vars, errs)
			right := eval(part[idx+2:], vars, errs)
			if !looseEqual(left, right) {
				return true
			}
			continue
		}
		if isTruthy(eval(part, vars, errs)) {
			return true
		}
	}
	return false
}

func findOperator(s, op string) int {
	return strings.Index(s, op)
}
