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
	"unicode/utf8"
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

// allowedGeneratedFullPaths is a small allowlist of known, singular
// theme-root config files the AI may propose a full-content update to,
// bypassing the extension check above — currently just defaults.json,
// checked by exact theme-root path (not a directory/glob). pages.json is
// deliberately NOT here: it's a structured registry the AI registers pages
// into via page_registry_entry (see propose_changes' schema), never a raw
// overwrite target — a full-file replacement there would silently drop
// every other page's registration. Without this, a brand/color/font
// request has no valid mode to run in: GenerationModeBrand's own validator
// (validateBrandModeProposal) is the only other place that allows
// defaults.json, but nothing sets that mode automatically (see
// themebuild.GenerateInput.Mode's doc comment on why it must be explicit),
// so nothing besides this allowlist made defaults.json reachable at all in
// the default (empty/edit) mode every existing chat actually runs in.
var allowedGeneratedFullPaths = map[string]bool{
	"defaults.json": true,
}

// maxPathLen matches chat_generated_files.file_path's column width
// (VARCHAR(500)) — counted in runes, like MySQL's utf8mb4 VARCHAR does,
// not bytes; a path with many multi-byte characters could fit the column
// but fail a byte-length check, or vice versa. Checked before any other
// rule, and without echoing the value back — a model proposal that goes
// badly wrong can put an entire file's content where a path belongs (seen
// in practice: a multi-KB JS blob in the path field), and nothing
// downstream (the DB write, the merchant-facing error, this log line)
// should have to hold that just to say "this path is broken".
const maxPathLen = 500

// previewLen bounds how much of an untrusted path gets echoed back in the
// remaining error messages below, for the same reason — a bad path can
// still be under maxPathLen and worth showing, just not showing all of.
const previewLen = 120

func preview(s string) string {
	r := []rune(s)
	if len(r) <= previewLen {
		return s
	}
	return string(r[:previewLen]) + "…"
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

// ValidateGeneratedFilePath additionally restricts to the extensions an AI
// proposal is allowed to write, on top of ValidatePathSafety's traversal
// checks. The model's output is treated as untrusted input here even though
// it isn't adversarial — see THEME_ENGINE_SPEC.md hard rule enforcement.
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

// ValidateThemeSlug guards the other half of a theme-relative path: the
// slug itself must be a plain directory-name-safe token, since it becomes a
// path segment on disk (see disk.go).
func ValidateThemeSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("theme slug must not be empty")
	}
	if slug != path.Clean(slug) || strings.ContainsAny(slug, "/\\") {
		return fmt.Errorf("invalid theme slug: %q", preview(slug))
	}
	return nil
}
