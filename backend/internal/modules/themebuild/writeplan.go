package themebuild

import (
	"context"
	"fmt"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"

	"golang.org/x/sync/errgroup"
)

// One file staged/written, paired with prior content for audit row.
type writtenFile struct {
	generated ai.GeneratedFile
	previous  *string
	kind      GeneratedFileKind
	pageMeta  *themefs.PageMeta
}

// One file writePlan will commit; pageMeta set for page.liquid (backend upserts pages.json).
type planFile struct {
	path     string
	action   FileAction
	content  string
	previous *string
	pageMeta *themefs.PageMeta
}

// Everything one turn needs to commit, computed in-memory before writing.
type writePlan struct {
	files       []planFile
	layoutStart *planFile
	layoutEnd   *planFile
}

func (p writePlan) paths() []string {
	paths := make([]string, 0, len(p.files)+2)
	for _, f := range p.files {
		paths = append(paths, f.path)
	}
	for _, f := range []*planFile{p.layoutStart, p.layoutEnd} {
		if f != nil {
			paths = append(paths, f.path)
		}
	}
	return paths
}

// Reads-only computation; failure leaves theme untouched (no partial silent modifications).
func (s *Service) buildWritePlan(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, result *ai.Result) (writePlan, error) {
	var plan writePlan

	// Concurrent reads (inside themeLocks) to avoid serializing other chats; order by index.
	if len(result.Files) > 0 {
		files := make([]planFile, len(result.Files))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(loadThemeFilesConcurrency)
		for i, f := range result.Files {
			g.Go(func() error {
				// Previous must be drafted content, not last-applied, or revert restores wrong state.
				previous, err := store.ReadFile(gctx, storeAuth, f.Path)
				if err != nil {
					return fmt.Errorf("read %q: %w", f.Path, err)
				}
				var previousPtr *string
				if previous != "" {
					previousPtr = &previous
				}
				files[i] = planFile{
					path:     f.Path,
					action:   FileAction(f.Action),
					content:  f.Content,
					previous: previousPtr,
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return writePlan{}, err
		}
		plan.files = files
	}

	if result.PageRegistryEntry != nil {
		entry := result.PageRegistryEntry
		// entry.Path is a route prefix, never a file path; derive the actual path from page basename.
		wantPath := "pages/" + entry.Page + ".liquid"
		if entry.Path == "/pages/auth" {
			wantPath = "pages/auth/" + entry.Page + ".liquid"
		}
		matched := false
		for i := range plan.files {
			if plan.files[i].path != wantPath {
				continue
			}
			plan.files[i].pageMeta = &themefs.PageMeta{
				Title:          entry.Title,
				Slug:           entry.Slug,
				Type:           entry.Type,
				Status:         entry.Status,
				SEOTitle:       entry.SEOTitle,
				SEODescription: entry.SEODescription,
				SEOKeywords:    entry.SEOKeywords,
				OGTitle:        entry.OGTitle,
				OGDescription:  entry.OGDescription,
				OGImagePath:    entry.OGImagePath,
			}
			matched = true
			break
		}
		if !matched {
			return writePlan{}, fmt.Errorf("register page: page_registry_entry (page %q, path %q) has no matching proposed file at %q", entry.Page, entry.Path, wantPath)
		}
	}

	// A turn that directly edits layout-start/end.liquid has taken full ownership of its content;
	if len(result.LayoutLinksToAdd) > 0 && !hasDirectEdit(plan.files, pathLayoutStart) {
		current, err := store.ReadFile(ctx, storeAuth, pathLayoutStart)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout css links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutLinksToAdd {
			updated, changed, err := themefs.AddStylesheetLink(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout css link %q: %w", path, err)
			}
			if changed {
				current = updated // so a second link in the same turn splices against the first
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutStart = &planFile{path: pathLayoutStart, content: current}
		}
	}

	if len(result.LayoutScriptsToAdd) > 0 && !hasDirectEdit(plan.files, pathLayoutEnd) {
		current, err := store.ReadFile(ctx, storeAuth, pathLayoutEnd)
		if err != nil {
			return writePlan{}, fmt.Errorf("add layout js links: %w", err)
		}
		changedAny := false
		for _, path := range result.LayoutScriptsToAdd {
			updated, changed, err := themefs.AddDeferredScript(current, path)
			if err != nil {
				return writePlan{}, fmt.Errorf("add layout js link %q: %w", path, err)
			}
			if changed {
				current = updated
				changedAny = true
			}
		}
		if changedAny {
			plan.layoutEnd = &planFile{path: pathLayoutEnd, content: current}
		}
	}

	return plan, nil
}

// hasDirectEdit reports whether files already has an entry for path.
func hasDirectEdit(files []planFile, path string) bool {
	for _, f := range files {
		if f.path == path {
			return true
		}
	}
	return false
}

// commitWritePlan writes everything in plan. Only plan.files gets an audit trail; layout files
// are shared, structurally-spliced config, not "generated files" of their own.
func (s *Service) commitWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, plan writePlan) ([]writtenFile, error) {
	written := make([]writtenFile, 0, len(plan.files))
	for _, f := range plan.files {
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, f.pageMeta); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
		written = append(written, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(f.action), Content: f.content},
			previous:  f.previous,
		})
	}
	for _, f := range []*planFile{plan.layoutStart, plan.layoutEnd} {
		if f == nil {
			continue
		}
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, nil); err != nil {
			return written, fmt.Errorf("write %q: %w", f.path, err)
		}
	}
	return written, nil
}

// planToStaged converts a writePlan into writtenFile shape for staging, without writing. Unlike
// commitWritePlan it also includes the layout splices — unaudited, they'd be lost until Apply.
func planToStaged(plan writePlan) []writtenFile {
	staged := make([]writtenFile, 0, len(plan.files)+2)
	for _, f := range plan.files {
		staged = append(staged, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(f.action), Content: f.content},
			previous:  f.previous,
			kind:      GeneratedFileKindProposed,
			pageMeta:  f.pageMeta,
		})
	}
	for _, f := range []*planFile{plan.layoutStart, plan.layoutEnd} {
		if f == nil {
			continue
		}
		staged = append(staged, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(FileActionUpdate), Content: f.content},
			kind:      GeneratedFileKindLayout,
		})
	}
	return staged
}
