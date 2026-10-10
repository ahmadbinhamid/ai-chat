package themebuild

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode"

	"ai-chat/internal/ai"
	"ai-chat/internal/prefetch"
	"ai-chat/internal/themefs"
)

const maxPreviewContextLen = 512

// sanitizePreviewContext drops values the client got wrong instead of failing the send: they only tune preloading.
func sanitizePreviewContext(route *string, focusFile string) (*string, string) {
	if route != nil {
		r := strings.TrimSpace(*route)
		if len(r) > maxPreviewContextLen || strings.IndexFunc(r, unicode.IsControl) >= 0 {
			route = nil
		} else {
			route = &r
		}
	}
	focusFile = strings.TrimSpace(focusFile)
	if len(focusFile) > maxPreviewContextLen || themefs.ValidatePathSafety(focusFile) != nil {
		focusFile = ""
	}
	return route, focusFile
}

// preloadFiles picks and reads, through the turn's draft overlay, the files this turn will almost certainly read.
func (s *Service) preloadFiles(ctx context.Context, in GenerateInput, tc ai.ThemeContext, store themefs.ThemeStore, storeAuth themefs.RequestAuth) prefetch.Result {
	// Brand turns only edit defaults.json, which is already in context.
	if in.Mode == ai.GenerationModeBrand || !s.preloadEnabledFor(in.model.ModelID) {
		return prefetch.Result{}
	}
	pin := prefetch.Input{
		FocusFile: in.FocusFile,
		PagesJSON: tc.PagesJSON,
		Prompt:    in.Prompt,
		Skip:      func(p string) bool { return tc.StagedImagePaths[p] },
	}
	if in.PreviewRoute != nil {
		pin.Route, pin.HasRoute = *in.PreviewRoute, true
	}
	return prefetch.Select(ctx, pin, func(ctx context.Context, p string) (string, error) {
		return store.ReadFile(ctx, storeAuth, p)
	})
}

// preloadEnabledFor reads the catalogue's per-model switch; with no catalogue (tests) or an unknown id, preload stays on.
func (s *Service) preloadEnabledFor(modelID string) bool {
	if s.models == nil {
		return true
	}
	m, ok := s.models.Model(modelID)
	return !ok || m.PreloadEnabled()
}

func logPreload(chatID string, r prefetch.Result) {
	paths := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	slog.Info("preloaded theme files for the first prompt", "chat_id", chatID,
		"preloaded_files", len(r.Files), "preloaded_bytes", r.Bytes, "paths", paths)
}

const preloadHeading = "Current contents of files you'll likely need (you can edit these via propose_changes without reading them first):"

// preloadBlock goes in the user turn, not the cached system block: it changes with every page the merchant views.
func preloadBlock(r prefetch.Result) string {
	if len(r.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(preloadHeading)
	b.WriteString("\n\n")
	for _, f := range r.Files {
		fmt.Fprintf(&b, "### %s\n%s\n\n", f.Path, f.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}

// preloadedContents maps each preloaded path to the content the model was given.
func preloadedContents(r prefetch.Result) map[string]string {
	if len(r.Files) == 0 {
		return nil
	}
	m := make(map[string]string, len(r.Files))
	for _, f := range r.Files {
		m[f.Path] = f.Content
	}
	return m
}

func alreadyProvidedNote(path string) string {
	return fmt.Sprintf("### %s\n(already provided above in \"Current contents of files you'll likely need\" and unchanged since — use that copy)\n\n", path)
}

// SetLargeThemeLimits sets when the prompt summarises a theme instead of listing it whole. Call once before serving.
func (s *Service) SetLargeThemeLimits(l ai.LargeThemeLimits) {
	s.largeTheme = l
}

func (s *Service) largeThemeLimits() ai.LargeThemeLimits {
	if s.largeTheme == (ai.LargeThemeLimits{}) {
		return ai.DefaultLargeThemeLimits
	}
	return s.largeTheme
}

// summariseLargeTheme switches a large theme's prompt to its compact form, keeping what this turn's files render.
func (s *Service) summariseLargeTheme(tc *ai.ThemeContext, preload prefetch.Result, draft map[string]string) {
	if !ai.IsLargeTheme(tc.PagesJSON, tc.FileTree, s.largeThemeLimits()) {
		return
	}
	tc.Compact = true
	rendered := map[string]bool{}
	for _, f := range preload.Files {
		tc.PreloadedPaths = append(tc.PreloadedPaths, f.Path)
		for _, p := range prefetch.RenderedPaths(f.Content) {
			rendered[p] = true
		}
	}
	for _, content := range draft {
		for _, p := range prefetch.RenderedPaths(content) {
			rendered[p] = true
		}
	}
	for p := range rendered {
		tc.RenderedComponents = append(tc.RenderedComponents, p)
	}
	sort.Strings(tc.PreloadedPaths)
	sort.Strings(tc.RenderedComponents)
}
