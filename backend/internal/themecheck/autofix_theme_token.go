package themecheck

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// AutoFixThemeTokens mechanically resolves the two deterministic shapes of
// a theme-token (rule 8) error finding — a var(--theme-*)/var(--layout-*)
// reference with no fallback, and a raw hex/rgb color literal used directly
// in a color property — without spending a model round-trip on what's
// usually a one-line substitution. Same pattern as
// AutoFixMissingBoilerplate/AutoFixMissingAssetRegistration: fix
// mechanically, let the caller re-run Check, only ask the model for
// whatever's genuinely left.
//
// Scoped to EXACTLY findings — never independently re-scans file content
// the way checkThemeToken itself does to decide WHAT counts as a
// violation. checkThemeToken grandfathers a declaration byte-identical to
// the file's pre-edit content (see its own doc comment); re-deriving that
// grandfathering logic here would risk silently diverging from it and
// rewriting a declaration the rule never actually flagged — the same class
// of bug DowngradePreExistingFindings exists to prevent elsewhere in this
// package. Instead, findings' Path+Offset say precisely which declarations
// are real, non-grandfathered violations; this function then re-runs the
// exact same regexes checkThemeToken itself uses, and only accepts a match
// whose start offset (m[0]) is one of those flagged ones. Scoping by byte
// offset rather than line matters: checkThemeToken's declRe has no newline
// anchor, so two declarations can share one line with only one of them
// flagged (new) and the other grandfathered (pre-existing) — line-level
// scoping can't tell them apart and would rewrite both. A match at a
// flagged offset is guaranteed to be a real violation — grandfathering is
// never reimplemented, only deferred to.
//
// Both fix kinds resolve against defaults.json's colors map only — never
// font/layout/header/etc. — matching this whole feature's scope (hardcoded
// COLORS specifically, the entire premise of rule 8's existence). A
// var(--theme-font-family) or var(--layout-radius) reference with no
// fallback, or a raw color matching no colors.* value, is left for the
// model to resolve, exactly like an unfixable case already is — guessing a
// non-color fallback, or a nearest-color match, is precisely the kind of
// invention this function must not do.
func AutoFixThemeTokens(p Proposal, snap Snapshot, findings []Finding) (fixed map[string]string, anyFixed bool) {
	colors, ok := parseDefaultsColors(snap.DefaultsJSON())
	if !ok {
		return nil, false
	}

	flaggedOffsets := make(map[string]map[int]bool) // path -> flagged byte offsets
	for _, f := range findings {
		if f.Rule != ruleIDThemeToken || f.Severity != SeverityError || f.Line <= 0 {
			continue
		}
		if flaggedOffsets[f.Path] == nil {
			flaggedOffsets[f.Path] = make(map[int]bool)
		}
		flaggedOffsets[f.Path][f.Offset] = true
	}

	fixed = make(map[string]string)
	var paths []string
	totalFixed := 0
	for _, f := range p.Files {
		offsets := flaggedOffsets[f.Path]
		if len(offsets) == 0 {
			continue
		}
		if newContent, count, ok := autoFixThemeTokensInFile(f.Content, offsets, colors); ok {
			fixed[f.Path] = newContent
			anyFixed = true
			paths = append(paths, f.Path)
			totalFixed += count
		}
	}
	if !anyFixed {
		return nil, false
	}
	slog.Info("themecheck: auto-fixed findings", "rule", ruleIDThemeToken, "paths", paths, "fixed_count", totalFixed)
	return fixed, true
}

// themeTokenFix is one byte-range replacement — start/end are offsets into
// the ORIGINAL content passed to autoFixThemeTokensInFile, computed before
// any fix in the same file is applied (see that function's own doc
// comment on why offsets are never recomputed mid-way through).
type themeTokenFix struct {
	start, end  int
	replacement string
}

// autoFixThemeTokensInFile finds every fixable var()-no-fallback and raw-
// color-in-a-color-property match starting at a flagged offset, then
// applies them all at once, back-to-front by start offset. All fix ranges
// are computed
// against the SAME original, unmodified content — never against a
// partially-patched copy — so every offset stays valid regardless of how
// many earlier (lower-offset) fixes are still pending; applying back-to-
// front (highest offset first) means a fix already applied can never shift
// the position of one that hasn't been yet. Fix 1's and Fix 2's own ranges
// never overlap by construction: Fix 1 only ever targets a bare var(...)
// call with no color content inside it, and Fix 2 only ever targets a raw
// color OUTSIDE any var(...) call (mirroring checkThemeToken's own
// withoutVarCalls exclusion) — so a color Fix 1 is about to insert as a
// fallback can never be mistaken by Fix 2 for a pre-existing raw literal,
// without either fix needing to know about the other.
func autoFixThemeTokensInFile(content string, flaggedOffsets map[int]bool, colors map[string]string) (result string, fixCount int, ok bool) {
	var fixes []themeTokenFix

	for _, m := range themeVarNoFallbackRe.FindAllStringSubmatchIndex(content, -1) {
		if !flaggedOffsets[m[0]] {
			continue
		}
		token := content[m[2]:m[3]] // e.g. "--theme-footer-bg"
		value, ok := resolveThemeVarColor(token, colors)
		if !ok {
			continue // not a colors.* token (font/layout/unrecognized) — leave for the model
		}
		fixes = append(fixes, themeTokenFix{start: m[0], end: m[1], replacement: fmt.Sprintf("var(%s, %s)", token, value)})
	}

	for _, m := range declRe.FindAllStringSubmatchIndex(content, -1) {
		if !flaggedOffsets[m[0]] {
			continue
		}
		prop := strings.ToLower(strings.TrimSpace(content[m[2]:m[3]]))
		if !colorProperties[prop] {
			continue
		}
		valueStart, valueEnd := m[4], m[5]
		for _, loc := range findRawColorsOutsideVarCalls(content[valueStart:valueEnd], valueStart) {
			raw := content[loc[0]:loc[1]]
			key, ok := findColorToken(colors, raw)
			if !ok {
				continue // matches no known token — leave it, don't guess
			}
			fixes = append(fixes, themeTokenFix{
				start: loc[0], end: loc[1],
				replacement: fmt.Sprintf("var(--theme-%s, %s)", camelToKebab(key), raw),
			})
		}
	}

	if len(fixes) == 0 {
		return "", 0, false
	}

	sort.Slice(fixes, func(i, j int) bool { return fixes[i].start > fixes[j].start })
	for _, fx := range fixes {
		content = content[:fx.start] + fx.replacement + content[fx.end:]
	}
	return content, len(fixes), true
}

// findRawColorsOutsideVarCalls returns the [start,end] byte ranges — in the
// coordinates of the OUTER content valueOffset was taken from, not value's
// own — of every hexOrRGBRe match in value that does not fall inside a
// var(...) call. Mirrors checkThemeToken's own withoutVarCalls exclusion
// (see hexOrRGBRe/varCallRe in rule_theme_token.go), just returning
// positions instead of only a boolean.
func findRawColorsOutsideVarCalls(value string, valueOffset int) [][2]int {
	varRanges := varCallRe.FindAllStringIndex(value, -1)
	insideVarCall := func(start, end int) bool {
		for _, vr := range varRanges {
			if start >= vr[0] && end <= vr[1] {
				return true
			}
		}
		return false
	}

	var out [][2]int
	for _, m := range hexOrRGBRe.FindAllStringIndex(value, -1) {
		if insideVarCall(m[0], m[1]) {
			continue
		}
		out = append(out, [2]int{valueOffset + m[0], valueOffset + m[1]})
	}
	return out
}

// resolveThemeVarColor resolves a "--theme-<kebab>"/"--layout-<kebab>"
// token (themeVarNoFallbackRe's captured group) against colors. Only a
// "--theme-*" token can ever resolve — see AutoFixThemeTokens' own doc
// comment on why this is scoped to colors.* and therefore never resolves a
// "--layout-*" reference (defaults.json's layout.* values — radius,
// spacing, shadow — aren't colors at all).
func resolveThemeVarColor(token string, colors map[string]string) (string, bool) {
	suffix, ok := strings.CutPrefix(token, "--theme-")
	if !ok {
		return "", false
	}
	value, ok := colors[kebabToCamel(suffix)]
	return value, ok
}

// findColorToken reverse-looks-up raw (a raw color literal from a
// declaration) against colors, returning the matching key. When more than
// one key shares the same value, the lexicographically first key is
// chosen — deterministic and arbitrary in exactly the same spirit as which
// key a merchant would have picked by hand; the substituted VALUE is
// identical either way, so which name wins only matters for readability,
// never correctness.
func findColorToken(colors map[string]string, raw string) (key string, ok bool) {
	var candidates []string
	for k, v := range colors {
		if colorValuesEqual(v, raw) {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Strings(candidates)
	return candidates[0], true
}

// colorValuesEqual compares two CSS color literals for AutoFixThemeTokens'
// reverse lookup. A hex value on both sides is compared after
// normalizeOpaqueHex (case and 3-vs-6-digit form ignored); anything else
// (rgb()/rgba(), or a hex form normalizeOpaqueHex refuses — 4-digit
// #rgba/8-digit #rrggbbaa, which carry alpha) is compared as a literal,
// trimmed string — no hex<->rgb conversion is attempted, and an
// alpha-carrying value never matches an opaque defaults.json color even if
// the RGB channels agree.
func colorValuesEqual(a, b string) bool {
	an, aok := normalizeOpaqueHex(a)
	bn, bok := normalizeOpaqueHex(b)
	if aok && bok {
		return an == bn
	}
	if aok != bok {
		return false
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// normalizeOpaqueHex lowercases and expands a 3-digit hex color to 6-digit
// so #FFF, #ffffff and #FFFFFF all compare equal. ok is false for anything
// that isn't a plain 3- or 6-digit hex color — in particular a 4-digit
// (#rgba) or 8-digit (#rrggbbaa) hex, which carries alpha and so can never
// represent the same color as an opaque defaults.json value, and anything
// that isn't hex at all (rgb()/rgba()).
func normalizeOpaqueHex(s string) (string, bool) {
	if !strings.HasPrefix(s, "#") {
		return "", false
	}
	hex := s[1:]
	switch len(hex) {
	case 3:
		hex = strings.ToLower(hex)
		return "#" + string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]}), true
	case 6:
		return "#" + strings.ToLower(hex), true
	default:
		return "", false
	}
}

// kebabToCamel converts a kebab-case CSS custom-property suffix (e.g.
// "footer-bg") to the camelCase key convention defaults.json's colors map
// uses ("footerBg") — confirmed against a real theme's defaults.json/CSS
// pairing (colors.footerBg <-> --theme-footer-bg), the standard,
// unambiguous camelCase<->kebab-case correspondence platform-side
// generation already uses (see theme_engine_spec.md §6).
func kebabToCamel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// camelToKebab converts a defaults.json colors key (e.g. "footerBg") to
// the kebab-case suffix its --theme-* custom property uses ("footer-bg") —
// the inverse of kebabToCamel, same convention.
func camelToKebab(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i > 0 && c >= 'A' && c <= 'Z' {
			b.WriteByte('-')
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// parseDefaultsColors extracts defaults.json's colors map. ok is false for
// missing or unparseable defaults.json, or one with no colors at all —
// AutoFixThemeTokens then fixes nothing, the same safe no-op today's
// (pre-auto-fix) behavior already is; a Warn is logged so a systematically
// broken defaults.json (rather than a merely brand-new theme with none yet)
// is still visible somewhere.
func parseDefaultsColors(defaultsJSON string) (map[string]string, bool) {
	if defaultsJSON == "" {
		return nil, false
	}
	var parsed struct {
		Colors map[string]string `json:"colors"`
	}
	if err := json.Unmarshal([]byte(defaultsJSON), &parsed); err != nil {
		slog.Warn("auto-fix theme-token: could not parse defaults.json, fixing nothing", "error", err)
		return nil, false
	}
	if len(parsed.Colors) == 0 {
		return nil, false
	}
	return parsed.Colors, true
}
