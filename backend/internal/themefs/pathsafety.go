// Package themefs is the boundary between generated content and the real
// theme filesystem (THEME_ENGINE_SPEC.md's `pages/`, `components/`, `css/`,
// `js/`, `defaults.json`, `pages.json` layout). Every function that decides
// *what* to write (path validation, the pages.json merge, the layout-file
// splice) is pure — no *os.File, no disk access — so it's unit-testable the
// same way the sibling app-booking's solver package is: cheaply, without a
// database or filesystem fixture. Only disk.go actually touches disk.
package themefs

import (
	"fmt"
	"path"
	"strings"
)

// allowedGeneratedExtensions is deliberately narrow: pages.json is never
// written as a raw generated file (see AI_THEME_BUILDER_PROMPT.md — its
// entries are merged structurally via a register_page apply action instead,
// precisely to avoid two chats clobbering each other's routes with a
// whole-file overwrite). Widen this only if a new file kind is genuinely
// needed. This restriction applies ONLY to files the AI proposes writing —
// see ValidateGeneratedFilePath — not to path safety in general: internal
// code legitimately reads/writes pages.json and defaults.json directly
// (building AI context, merging a page registration), and those calls go
// through ValidatePathSafety instead, which has no extension opinion.
var allowedGeneratedExtensions = map[string]bool{
	".liquid": true,
	".css":    true,
	".js":     true,
}

// ValidatePathSafety rejects anything that isn't a plain, theme-root-relative
// path: no absolute paths, no ".." traversal (including a disguised
// backslash variant), nothing that escapes the theme root once cleaned.
// This is the baseline check for ANY theme-relative file access — reads of
// known config files (pages.json, defaults.json) included — regardless of
// what kind of file it is. Use ValidateGeneratedFilePath instead when
// specifically validating a path the AI proposed writing.
func ValidatePathSafety(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("file path must not be empty")
	}
	if path.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("file path must be theme-relative, got absolute path %q", relPath)
	}
	if strings.Contains(relPath, "\\") {
		return fmt.Errorf("file path must not contain backslashes: %q", relPath)
	}
	cleaned := path.Clean(relPath)
	if cleaned != relPath || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("file path escapes the theme root: %q", relPath)
	}
	return nil
}

// ValidateGeneratedFilePath additionally restricts to the extensions an AI
// proposal is allowed to write, on top of ValidatePathSafety's traversal
// checks. The model's output is treated as untrusted input here even though
// it isn't adversarial — see THEME_ENGINE_SPEC.md hard rule enforcement.
func ValidateGeneratedFilePath(relPath string) error {
	if err := ValidatePathSafety(relPath); err != nil {
		return err
	}
	ext := path.Ext(path.Clean(relPath))
	if !allowedGeneratedExtensions[ext] {
		return fmt.Errorf("file extension %q is not allowed for a generated file (only .liquid, .css, .js)", ext)
	}
	return nil
}

// ValidateThemeSlug guards the other half of a theme-relative path: the
// slug itself must be a plain directory-name-safe token, since it becomes a
// path segment on disk (see disk.go).
func ValidateThemeSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("theme slug must not be empty")
	}
	if slug != path.Clean(slug) || strings.ContainsAny(slug, "/\\") {
		return fmt.Errorf("invalid theme slug: %q", slug)
	}
	return nil
}
