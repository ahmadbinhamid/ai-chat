package themecheck

import (
	"fmt"
	"strings"
)

const ruleIDPageBoilerplate = "page-boilerplate"

// wantLayoutStartParams is spec §3's exact 9-param layout-start render call
// — key and value both fixed, in this order. customer_authenticated's value
// is intentionally "auth_check", not "customer_authenticated" — that's the
// spec's own naming, not a typo.
var wantLayoutStartParams = []RenderParam{
	{Key: "page", Value: "page"},
	{Key: "store", Value: "store"},
	{Key: "menu", Value: "menu"},
	{Key: "path", Value: "path"},
	{Key: "theme", Value: "theme"},
	{Key: "customer", Value: "customer"},
	{Key: "customer_authenticated", Value: "auth_check"},
	{Key: "environment", Value: "environment"},
	{Key: "csrf_token", Value: "csrf_token"},
}

// wantLayoutEndParams is spec §3's exact layout-end render call.
var wantLayoutEndParams = []RenderParam{
	{Key: "theme", Value: "theme"},
	{Key: "store", Value: "store"},
}

// checkPageBoilerplate enforces rule 1: every pages/**/*.liquid file must
// open with the exact §3 layout-start render (all 9 params, unchanged
// order/values) and close with the exact layout-end render. Comparison is
// on the parsed render call's target and params, not raw text — §3's own
// examples show the same call both spread across lines (§3) and inlined on
// one (§4's home.liquid), so a byte-for-byte match would reject valid code.
func checkPageBoilerplate(p Proposal, _ Snapshot) []Finding {
	var findings []Finding
	for _, f := range p.Files {
		if !isPagesLiquidFile(f.Path) {
			continue
		}

		tags := ScanTags(f.Content)
		var renders []renderCall
		for _, t := range tags {
			if t.Name != "render" {
				continue
			}
			target, params, ok := ParseRenderTag(t.Raw)
			if !ok {
				continue
			}
			renders = append(renders, renderCall{target: target, params: params, raw: f.Content[t.Start:t.End]})
		}

		start := findRenderCall(renders, "liquid/layout-start")
		if start == nil {
			findings = append(findings, boilerplateFinding(f.Path,
				"missing the required 'liquid/layout-start' render — every pages/*.liquid file must open with exactly: "+
					"{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, "+
					"customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}"))
		} else if diff := paramsDiff(start.params, wantLayoutStartParams); diff != "" {
			findings = append(findings, boilerplateFinding(f.Path,
				"'liquid/layout-start' render params don't match the required §3 boilerplate exactly (params may not be added, "+
					"removed, or reordered): "+diff))
		}

		end := findRenderCall(renders, "liquid/layout-end")
		if end == nil {
			findings = append(findings, boilerplateFinding(f.Path,
				"missing the required 'liquid/layout-end' render — every pages/*.liquid file must close with exactly: "+
					"{% render 'liquid/layout-end', theme: theme, store: store %}"))
		} else if diff := paramsDiff(end.params, wantLayoutEndParams); diff != "" {
			findings = append(findings, boilerplateFinding(f.Path,
				"'liquid/layout-end' render params don't match the required §3 boilerplate exactly: "+diff))
		}
	}
	return findings
}

// layoutStartRenderTag/layoutEndRenderTag are §3's exact boilerplate, spelled
// out literally so AutoFixMissingBoilerplate's output is byte-for-byte what
// checkPageBoilerplate itself accepts (see wantLayoutStartParams/
// wantLayoutEndParams above — these two must stay in sync with those).
const layoutStartRenderTag = `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}`
const layoutEndRenderTag = `{% render 'liquid/layout-end', theme: theme, store: store %}`

// AutoFixMissingBoilerplate deterministically repairs both page-boilerplate
// failure modes that are mechanical rather than a real judgment call:
//   - a pages/*.liquid file missing the layout-start/layout-end render
//     entirely (in practice, the model regenerating an existing page's full
//     content and simply dropping the wrapper — observed repeatedly even
//     after the model was told to re-read the file first);
//   - a layout-start/layout-end render call that IS present but whose params
//     are wrong, reordered, or incomplete (in practice, a page-creation
//     prompt where the model composes the whole file from scratch instead of
//     editing something existing, and free-hands the boilerplate).
//
// Both are safe to fix by dumb text substitution, not just the first: §3
// mandates exactly one byte-exact call per file (wantLayoutStartParams/
// wantLayoutEndParams, mirrored literally in layoutStartRenderTag/
// layoutEndRenderTag), so there's only ever one correct replacement and no
// guessing about intent — unlike, say, a missing param elsewhere in the page
// where the right fix depends on what the page is trying to do.
//
// Returns the patched content for every file that needed a fix, keyed by
// path — callers apply it to their own copy of the proposal/result; this
// function never mutates p.
func AutoFixMissingBoilerplate(p Proposal) (fixed map[string]string, anyFixed bool) {
	fixed = make(map[string]string)
	for _, f := range p.Files {
		if !isPagesLiquidFile(f.Path) {
			continue
		}

		tags := ScanTags(f.Content)
		var renders []renderCall
		for _, t := range tags {
			if t.Name != "render" {
				continue
			}
			target, params, ok := ParseRenderTag(t.Raw)
			if !ok {
				continue
			}
			renders = append(renders, renderCall{target: target, params: params, raw: f.Content[t.Start:t.End]})
		}

		content := f.Content
		var startChanged, endChanged bool
		content, startChanged = fixLayoutRender(content, renders, "liquid/layout-start", wantLayoutStartParams,
			layoutStartRenderTag, true)
		content, endChanged = fixLayoutRender(content, renders, "liquid/layout-end", wantLayoutEndParams,
			layoutEndRenderTag, false)
		if startChanged || endChanged {
			fixed[f.Path] = content
			anyFixed = true
		}
	}
	return fixed, anyFixed
}

// fixLayoutRender applies one of the two mechanical fixes described on
// AutoFixMissingBoilerplate for a single render target: if the call is
// altogether missing, the canonical tag is inserted at the start (prepend)
// or end (append) of content; if it's present with the wrong params, its
// verbatim matched text is swapped for the canonical tag in place. Returns
// whether this target needed a fix, so the caller can OR it with the other
// target's result.
func fixLayoutRender(content string, renders []renderCall, target string, want []RenderParam, wantTag string,
	prepend bool) (string, bool) {
	call := findRenderCall(renders, target)
	switch {
	case call == nil:
		if prepend {
			content = wantTag + "\n" + content
		} else {
			content = strings.TrimRight(content, "\n") + "\n" + wantTag + "\n"
		}
		return content, true
	case !paramsEqual(call.params, want):
		// call.raw is this exact call's own matched "{% render ... %}" text
		// (captured per-occurrence above), so replacing just its first
		// occurrence can't accidentally touch an unrelated tag even if two
		// renders elsewhere happen to share substrings.
		return strings.Replace(content, call.raw, wantTag, 1), true
	default:
		return content, false
	}
}

func isPagesLiquidFile(path string) bool {
	return strings.HasPrefix(path, "pages/") && strings.HasSuffix(path, ".liquid") &&
		!strings.HasPrefix(path, "pages/css/")
}

// renderCall is a parsed {% render %} tag plus its own verbatim source text,
// so a fix can locate-and-replace exactly the occurrence that was parsed
// rather than the first substring match against a rebuilt string.
type renderCall struct {
	target string
	params []RenderParam
	raw    string
}

func findRenderCall(renders []renderCall, target string) *renderCall {
	for i := range renders {
		if renders[i].target == target {
			return &renders[i]
		}
	}
	return nil
}

// paramsDiff returns a human-readable description of how got differs from
// want (missing, extra, reordered, or wrong-value params), or "" if they
// match exactly.
func paramsDiff(got, want []RenderParam) string {
	if paramsEqual(got, want) {
		return ""
	}
	return fmt.Sprintf("got %s, want %s", formatParams(got), formatParams(want))
}

func paramsEqual(a, b []RenderParam) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func formatParams(params []RenderParam) string {
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = p.Key + ": " + p.Value
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func boilerplateFinding(path, message string) Finding {
	return Finding{Path: path, Rule: ruleIDPageBoilerplate, Severity: SeverityError, Message: message}
}
