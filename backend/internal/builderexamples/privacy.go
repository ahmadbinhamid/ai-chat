package builderexamples

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
)

var (
	tokenLikeRe = regexp.MustCompile(`(?i)\b(bearer\s+[a-z0-9\-._~+/]+=*|sk-[a-z0-9]{10,}|api[_-]?key\s*[:=]\s*\S+|password\s*[:=]\s*\S+|authorization\s*[:=]\s*\S+)\b`)
	emailRe     = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
)

// Fingerprint returns a stable SHA-256 hex of the normalized prompt.
// Never includes secrets-bearing raw text in the hash input beyond what the
// merchant typed (normalized whitespace only).
func Fingerprint(prompt string) string {
	n := normalizePrompt(prompt)
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

func normalizePrompt(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	return strings.Join(strings.Fields(p), " ")
}

// SanitizePrompt redacts credential-like spans and emails, then truncates.
// Used only when StoreSanitizedPrompt is explicitly enabled.
func SanitizePrompt(prompt string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 200
	}
	s := tokenLikeRe.ReplaceAllString(prompt, "[REDACTED]")
	s = emailRe.ReplaceAllString(s, "[EMAIL]")
	// Drop control chars.
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > maxRunes {
		s = string(runes[:maxRunes]) + "…"
	}
	return s
}

// ExtractProtectedFields pulls protect_field:* entries from plan constraints.
func ExtractProtectedFields(constraints []string) (protected, prefs, rest []string) {
	for _, c := range constraints {
		c = strings.TrimSpace(c)
		low := strings.ToLower(c)
		switch {
		case strings.HasPrefix(low, "protect_field:"):
			protected = append(protected, strings.TrimSpace(c[len("protect_field:"):]))
		case strings.HasPrefix(low, "preference:"):
			prefs = append(prefs, strings.TrimSpace(c[len("preference:"):]))
		default:
			rest = append(rest, c)
		}
	}
	return protected, prefs, rest
}
