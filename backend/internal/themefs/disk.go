package themefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store reads and writes theme files under a single configured root — one
// subdirectory per theme_slug, matching THEME_ENGINE_SPEC.md's layout. It is
// the only part of this package that touches disk; everything else
// (pathsafety.go, pages.go, layout.go) is pure and can be tested without one.
type Store struct {
	root string
}

// NewStore builds a Store rooted at root (config.ThemeStorageRoot).
func NewStore(root string) *Store {
	return &Store{root: root}
}

// resolve validates themeSlug and relPath, then returns the absolute path —
// re-checking with filepath.Rel afterwards as defense in depth, in case a
// future change to ValidatePathSafety's rules ever misses a traversal
// variant. Deliberately uses the extension-agnostic ValidatePathSafety, not
// ValidateGeneratedFilePath: Store is used for legitimate internal reads/
// writes of pages.json and defaults.json too, not just AI-generated
// .liquid/.css/.js files — callers that need the stricter, AI-proposal-only
// extension check apply it themselves before calling in (see
// themebuild.validateProposal / applyFiles).
func (s *Store) resolve(themeSlug, relPath string) (string, error) {
	if err := ValidateThemeSlug(themeSlug); err != nil {
		return "", err
	}
	if err := ValidatePathSafety(relPath); err != nil {
		return "", err
	}

	themeRoot := filepath.Join(s.root, themeSlug)
	abs := filepath.Join(themeRoot, relPath)

	rel, err := filepath.Rel(themeRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("resolved path escapes theme root: %q", relPath)
	}
	return abs, nil
}

// ReadFile returns a theme file's current content, or ("", nil) if it
// doesn't exist yet (e.g. reading pages.json for a theme that has none of
// its own custom pages registered yet is a normal, empty-registry case, not
// an error).
func (s *Store) ReadFile(themeSlug, relPath string) (string, error) {
	abs, err := s.resolve(themeSlug, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", relPath, err)
	}
	return string(data), nil
}

// WriteFile writes content to a theme file, creating parent directories as
// needed (a brand-new page's directory may not exist yet).
func (s *Store) WriteFile(themeSlug, relPath, content string) error {
	abs, err := s.resolve(themeSlug, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", relPath, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	return nil
}
