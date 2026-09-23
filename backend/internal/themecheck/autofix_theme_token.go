package themecheck

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// AutoFixThemeTokens mechanically fixes var()-no-fallback and raw-color findings from rule 8 against defaults.json's colors map, without a model round-trip.
// It only fixes flagged byte offsets, not a re-scan, since declRe has no newline anchor and two declarations can share a line with only one flagged.
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

// themeTokenFix is a byte-range replacement; start/end are offsets into the original, pre-fix content.
type themeTokenFix struct {
	start, end  int
	replacement string
}

// autoFixThemeTokensInFile finds every fixable match at a flagged offset and applies them back-to-front by start offset
// against the original, unmodified content, so earlier fixes never invalidate offsets of ones still pending.
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

// findRawColorsOutsideVarCalls returns byte ranges, in the outer content's coordinates, of raw color matches outside var(...) calls.
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

// resolveThemeVarColor resolves a "--theme-<kebab>" token against colors; "--layout-*" never resolves since layout values aren't colors.
func resolveThemeVarColor(token string, colors map[string]string) (string, bool) {
	suffix, ok := strings.CutPrefix(token, "--theme-")
	if !ok {
		return "", false
	}
	value, ok := colors[kebabToCamel(suffix)]
	return value, ok
}

// findColorToken reverse-looks-up raw against colors; when multiple keys share a value, the lexicographically first wins (arbitrary, doesn't affect correctness).
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

// colorValuesEqual compares hex values via normalizeOpaqueHex, else falls back to trimmed string equality; no hex<->rgb conversion, so an alpha-carrying value never matches an opaque one.
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

// normalizeOpaqueHex normalizes a 3- or 6-digit hex color; ok is false for anything else, including alpha-carrying 4/8-digit hex.
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

// kebabToCamel converts a kebab-case CSS custom-property suffix (e.g. "footer-bg") to defaults.json's colors key convention ("footerBg").
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

// camelToKebab converts a defaults.json colors key ("footerBg") to its --theme-* kebab-case suffix ("footer-bg") — the inverse of kebabToCamel.
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

// parseDefaultsColors extracts defaults.json's colors map; ok is false for missing/unparseable/empty, in which case AutoFixThemeTokens safely fixes nothing.
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
