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
		var renders []struct {
			target string
			params []RenderParam
		}
		for _, t := range tags {
			if t.Name != "render" {
				continue
			}
			target, params, ok := ParseRenderTag(t.Raw)
			if !ok {
				continue
			}
			renders = append(renders, struct {
				target string
				params []RenderParam
			}{target, params})
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

// AutoFixMissingBoilerplate deterministically repairs the one page-
// boilerplate failure mode that's mechanical rather than a real judgment
// call: a pages/*.liquid file that's missing the layout-start/layout-end
// render entirely (in practice, the model regenerating an existing page's
// full content and simply dropping the wrapper — observed repeatedly even
// after the model was told to re-read the file first). A render call
// present with the WRONG params is deliberately left alone — that's a
// different, more ambiguous failure this function doesn't try to guess how
// to fix — the retry-with-model-repair path (see themebuild's
// checkAndRepair) still handles that case.
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
		var renders []struct {
			target string
			params []RenderParam
		}
		for _, t := range tags {
			if t.Name != "render" {
				continue
			}
			target, params, ok := ParseRenderTag(t.Raw)
			if !ok {
				continue
			}
			renders = append(renders, struct {
				target string
				params []RenderParam
			}{target, params})
		}

		content := f.Content
		changed := false
		if findRenderCall(renders, "liquid/layout-start") == nil {
			content = layoutStartRenderTag + "\n" + content
			changed = true
		}
		if findRenderCall(renders, "liquid/layout-end") == nil {
			content = strings.TrimRight(content, "\n") + "\n" + layoutEndRenderTag + "\n"
			changed = true
		}
		if changed {
			fixed[f.Path] = content
			anyFixed = true
		}
	}
	return fixed, anyFixed
}

func isPagesLiquidFile(path string) bool {
	return strings.HasPrefix(path, "pages/") && strings.HasSuffix(path, ".liquid") &&
		!strings.HasPrefix(path, "pages/css/")
}

func findRenderCall(renders []struct {
	target string
	params []RenderParam
}, target string) *struct {
	target string
	params []RenderParam
} {
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
