package themebuild

import (
	"context"
	"fmt"

	"ai-chat/internal/ai"
	"ai-chat/internal/themefs"

	"golang.org/x/sync/errgroup"
)

// writtenFile is one proposed or layout file staged into the draft (see
// planToStaged) or, for Service.ApplyDraft, actually written to FlowPOS
// (see commitWritePlan) — paired with whatever content it replaced so the
// audit row persisted afterward (see persistFileRecords) can still record
// what changed, even after the real "before" state is gone. Despite the
// name (kept from when this only ever meant "already committed to disk" —
// see doGenerate's staging path, which never calls commitWritePlan at all
// now), the struct itself is just "here's what to audit," write or no write.
type writtenFile struct {
	generated ai.GeneratedFile
	previous  *string
	// kind/pageMeta are what the 20260813000001 migration's two new
	// columns exist for — see GeneratedFileKind and persistFileRecords.
	kind     GeneratedFileKind
	pageMeta *themefs.PageMeta
}

// planFile is one file a writePlan will commit — either a proposed file
// (action/content straight from the model) or the recomputed content of a
// shared file (a layout file) after folding in this turn's splice. previous
// is only meaningful for proposed files (see writtenFile) and is nil
// otherwise. pageMeta is set only when this file is the page.liquid file
// PageRegistryEntry describes — flowpos-backend's own theme-file API upserts
// pages.json itself from these fields (see themefs.Store.WriteFile), so this
// service no longer computes pages.json content directly.
type planFile struct {
	path     string
	action   FileAction
	content  string
	previous *string
	pageMeta *themefs.PageMeta
}

// writePlan is everything one turn needs to commit to the real theme,
// computed entirely in memory before anything is written — see
// buildWritePlan/commitWritePlan.
type writePlan struct {
	files       []planFile
	layoutStart *planFile
	layoutEnd   *planFile
}

// paths lists every path this plan will write, in the same order it's
// computed — used only for EventTypeStaged's narration payload; the write
// itself (commitWritePlan) doesn't need this, it walks the struct directly.
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

// buildWritePlan computes every file this turn would write — proposed
// files verbatim (with page metadata attached to the one matching
// PageRegistryEntry, if any), plus the layout-file splices — using only
// reads, never a write. Nothing is committed until commitWritePlan runs, so
// a failure here (a layout file missing its insertion marker, a page
// registry entry with no matching file) leaves the real theme completely
// untouched instead of partially, silently modified.
func (s *Service) buildWritePlan(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, result *ai.Result) (writePlan, error) {
	var plan writePlan

	// Reads run concurrently (errgroup, capped at 8 in flight) rather than
	// one HTTP round trip at a time — this whole call happens inside
	// themeLocks (see doGenerate), so a turn proposing a few dozen file
	// edits was serializing every OTHER chat's staging behind that many
	// sequential round trips to flowpos-backend. files is pre-sized and
	// written by index rather than appended, so the plan's file order
	// stays exactly result.Files' order regardless of which read finishes
	// first — order matters below (deterministic commitWritePlan writes).
	if len(result.Files) > 0 {
		files := make([]planFile, len(result.Files))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(loadThemeFilesConcurrency)
		for i, f := range result.Files {
			g.Go(func() error {
				// store here is the draft overlay (see doGenerate) —
				// "previous" must be the draft's own prior content (what
				// an earlier turn in THIS chat already staged), not the
				// last-applied theme, or revert-within-a-draft (see
				// RevertToMessage's updated doc comment) would restore
				// the wrong "before" state.
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
		// entry.Path is the route prefix ("/pages" or "/pages/auth"), never a
		// file path — the proposed file's actual theme-relative path has to be
		// derived from it the same way §5 requires: page == the .liquid file's
		// basename.
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

	if len(result.LayoutLinksToAdd) > 0 {
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

	if len(result.LayoutScriptsToAdd) > 0 {
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

// commitWritePlan writes everything in plan through flowpos-backend's own
// theme-file API (each individual write already atomic on its side — see
// themefs.Store.WriteFile). Only the proposed files (plan.files) get an
// audit trail (see persistFileRecords) — the layout files are shared,
// structurally-spliced config, not "generated files" in their own right.
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

// planToStaged converts a writePlan into the same writtenFile shape
// commitWritePlan's callers already know how to audit — used by
// doGenerate's staging path (no write, see its own comment) instead of
// commitWritePlan, which actually writes and is now only ever called by
// Service.ApplyDraft. Unlike commitWritePlan, this DOES include
// plan.layoutStart/layoutEnd (tagged GeneratedFileKindLayout) — see the
// 20260813000001 migration's doc comment for why an unaudited layout
// splice, harmless when writes were immediate, is a silent data-loss bug
// the moment the write is deferred: nothing else remembers the splice
// happened until Apply runs.
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
