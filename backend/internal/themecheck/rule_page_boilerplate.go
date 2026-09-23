package themecheck

import (
	"fmt"
	"log/slog"
	"strings"
)

const ruleIDPageBoilerplate = "page-boilerplate"

// wantLayoutStartParams is spec §3's exact layout-start render call, key/value/order fixed.
// customer_authenticated's value is intentionally "auth_check" per spec naming, not a typo.
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

// checkPageBoilerplate enforces rule 1: pages/**/*.liquid must open/close with the exact §3 layout-start/layout-end render.
// Compared on parsed target+params, not raw text — §3's examples show the same call both spread across lines and inlined.
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

// layoutStartRenderTag/layoutEndRenderTag are §3's exact boilerplate text; must stay in sync with wantLayoutStartParams/wantLayoutEndParams.
const layoutStartRenderTag = `{% render 'liquid/layout-start', page: page, store: store, menu: menu, path: path, theme: theme, customer: customer, customer_authenticated: auth_check, environment: environment, csrf_token: csrf_token %}`
const layoutEndRenderTag = `{% render 'liquid/layout-end', theme: theme, store: store %}`

// AutoFixMissingBoilerplate fixes a missing or malformed layout-start/layout-end render in pages/*.liquid.
// Safe as dumb text substitution: §3 mandates exactly one byte-exact call, so there's no guessing about intent. Never mutates p.
func AutoFixMissingBoilerplate(p Proposal) (fixed map[string]string, anyFixed bool) {
	fixed = make(map[string]string)
	var paths []string
	fixCount := 0
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
			paths = append(paths, f.Path)
			if startChanged {
				fixCount++
			}
			if endChanged {
				fixCount++
			}
		}
	}
	if anyFixed {
		slog.Info("themecheck: auto-fixed findings", "rule", ruleIDPageBoilerplate, "paths", paths, "fixed_count", fixCount)
	}
	return fixed, anyFixed
}

// fixLayoutRender inserts the canonical tag if missing (prepend/append per position), or swaps it in if params are wrong.
// Returns whether this target needed a fix, so the caller can OR it with the other target's result.
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
		// call.raw is this call's own matched text, so replacing its first occurrence can't touch an unrelated tag.
		return strings.Replace(content, call.raw, wantTag, 1), true
	default:
		return content, false
	}
}

func isPagesLiquidFile(path string) bool {
	return strings.HasPrefix(path, "pages/") && strings.HasSuffix(path, ".liquid") &&
		!strings.HasPrefix(path, "pages/css/")
}

// renderCall is a parsed {% render %} tag plus its own verbatim source text, so a fix can replace exactly this occurrence.
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

// paramsDiff describes how got differs from want (missing/extra/reordered/wrong-value), or "" if they match.
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
