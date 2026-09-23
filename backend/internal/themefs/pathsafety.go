// Package themefs is the boundary between generated content and the real theme filesystem; path/merge logic is pure (no disk) except disk.go.
package themefs

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"
)

// allowedGeneratedExtensions is a strict allowlist of real theme file kinds — hard-rejects other tech stacks (.php/.jsx/.py/etc.), enforcing §11's "must not" rule at the code level.
// Applies only to AI-proposed writes (ValidateGeneratedFilePath); internal reads/writes of pages.json/defaults.json go through ValidatePathSafety instead, which has no extension opinion.
var allowedGeneratedExtensions = map[string]bool{
	".liquid": true,
	".css":    true,
	".js":     true,
	".json":   true,
}

// allowedGeneratedFullPaths is a small allowlist of extensionless theme-root files (e.g. robots.txt), checked by exact
// path, not glob — widen only for another genuinely singular theme-root file, never as a general escape hatch.
var allowedGeneratedFullPaths = map[string]bool{
	"robots.txt": true,
}

// maxPathLen matches chat_generated_files.file_path's VARCHAR(500), counted in runes like MySQL's utf8mb4, not bytes.
// Checked first, without echoing the value back — a bad proposal can put an entire file's content where a path belongs.
const maxPathLen = 500

// previewLen bounds how much of an untrusted path gets echoed back in error messages below.
const previewLen = 120

func preview(s string) string {
	r := []rune(s)
	if len(r) <= previewLen {
		return s
	}
	return string(r[:previewLen]) + "…"
}

// ValidatePathSafety rejects anything that isn't a plain, theme-root-relative path — no absolute paths, no ".." traversal
// (including a disguised backslash variant), nothing that escapes the theme root once cleaned. Baseline check for ANY theme-relative access.
func ValidatePathSafety(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("file path must not be empty")
	}
	if n := utf8.RuneCountInString(relPath); n > maxPathLen {
		return fmt.Errorf("file path is too long (%d characters, max %d) — this usually means content ended up where a path belongs", n, maxPathLen)
	}
	if path.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("file path must be theme-relative, got absolute path %q", preview(relPath))
	}
	if strings.Contains(relPath, "\\") {
		return fmt.Errorf("file path must not contain backslashes: %q", preview(relPath))
	}
	cleaned := path.Clean(relPath)
	if cleaned != relPath || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("file path escapes the theme root: %q", preview(relPath))
	}
	return nil
}

// ValidateGeneratedFilePath additionally restricts to allowed extensions on top of ValidatePathSafety's traversal checks — the model's output is treated as untrusted input here.
func ValidateGeneratedFilePath(relPath string) error {
	if err := ValidatePathSafety(relPath); err != nil {
		return err
	}
	if allowedGeneratedFullPaths[relPath] {
		return nil
	}
	ext := path.Ext(path.Clean(relPath))
	if !allowedGeneratedExtensions[ext] {
		return fmt.Errorf("file extension %q is not allowed for a generated file (only .liquid, .css, .js): %q", ext, preview(relPath))
	}
	return nil
}

// ValidateThemeSlug guards the other half of a theme-relative path — the slug must be a plain directory-name-safe token.
func ValidateThemeSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("theme slug must not be empty")
	}
	if slug != path.Clean(slug) || strings.ContainsAny(slug, "/\\") {
		return fmt.Errorf("invalid theme slug: %q", preview(slug))
	}
	return nil
}
