package themebuild

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
// files verbatim (with page metadata attached to files matching
// PageRegistryEntry / extraEntries), plus the layout-file splices — using
// only reads, never a write. Nothing is committed until commitWritePlan runs, so
// a failure here (a layout file missing its insertion marker, a page
// registry entry with no matching file) leaves the real theme completely
// untouched instead of partially, silently modified.
//
// extraEntries lets compound workflows register one page per atomic step
// without forcing a full pages.json rewrite (canonical single-page contract
// is page_registry_entry → PageMeta on the liquid file).
func (s *Service) buildWritePlan(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, result *ai.Result, extraEntries ...*themefs.PageEntry) (writePlan, error) {
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
				if files[i].action == FileActionDelete {
					files[i].content = DraftDeleteMarker
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return writePlan{}, err
		}
		plan.files = files
	}

	entries := make([]*themefs.PageEntry, 0, 1+len(extraEntries))
	if result.PageRegistryEntry != nil {
		entries = append(entries, result.PageRegistryEntry)
	}
	entries = append(entries, extraEntries...)
	seenPath := map[string]bool{}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		wantPath := pageRegistryWantPath(entry)
		if seenPath[wantPath] {
			continue
		}
		seenPath[wantPath] = true
		if err := attachPageRegistryEntry(&plan, entry); err != nil {
			return writePlan{}, err
		}
	}

	// layout-start.liquid/layout-end.liquid are directly editable now (a
	// files[] entry for either is no longer rejected — see proposal.go's
	// own doc comment on why that check was removed). A turn that directly
	// edits one of them has, by doing so, taken full ownership of that
	// file's content for this turn: skip computing a splice for the same
	// path here rather than layering it on top. Two reasons, not one — the
	// documented production crash (a duplicate chat_generated_files audit
	// row for the same (message_id, file_path), see planToStaged) is the
	// smaller of them; the real one is that commitWritePlan writes
	// plan.files first and a layout splice after, against content read
	// BEFORE the direct edit landed — applying it anyway would silently
	// overwrite the model's own direct edit with stale content plus the
	// spliced tag, not just double an audit row.
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

// hasDirectEdit reports whether files already has an entry for path —
// checked before computing a layout splice for that same path (see
// buildWritePlan's own doc comment above).
func hasDirectEdit(files []planFile, path string) bool {
	for _, f := range files {
		if f.path == path {
			return true
		}
	}
	return false
}

// commitWritePlan writes everything in plan through flowpos-backend's own
// theme-file API (each individual write already atomic on its side — see
// themefs.Store.WriteFile). Only the proposed files (plan.files) get an
// audit trail (see persistFileRecords) — the layout files are shared,
// structurally-spliced config, not "generated files" in their own right.
//
// If any write fails, already-written files in this plan are rolled back
// (restore PreviousContent, or DeleteFile for creates without previous)
// so Apply never leaves a half-updated live theme with draft still pending.
func (s *Service) commitWritePlan(ctx context.Context, storeAuth themefs.RequestAuth, plan writePlan) ([]writtenFile, error) {
	type appliedStep struct {
		path     string
		previous *string
		action   FileAction
		pageMeta *themefs.PageMeta
		isLayout bool
	}
	var steps []appliedStep
	written := make([]writtenFile, 0, len(plan.files))

	rollback := func(failedPath string, writeErr error) error {
		slog.Warn("ai: apply rollback started",
			"failed_path", failedPath, "written_before_fail", len(steps), "error", writeErr.Error())
		for i := len(steps) - 1; i >= 0; i-- {
			st := steps[i]
			var rbErr error
			switch {
			case st.action == FileActionDelete:
				// Undo delete → restore previous content if we had it.
				if st.previous != nil {
					rbErr = s.store.WriteFile(ctx, storeAuth, st.path, *st.previous, st.pageMeta)
					slog.Info("ai: apply rollback restore-after-delete", "path", st.path, "error", errString(rbErr))
				} else {
					slog.Warn("ai: apply rollback skipped — deleted file had no previous content", "path", st.path)
				}
			case st.action == FileActionCreate && (st.previous == nil || *st.previous == ""):
				rbErr = s.store.DeleteFile(ctx, storeAuth, st.path)
				slog.Info("ai: apply rollback delete", "path", st.path, "error", errString(rbErr))
			case st.previous != nil:
				rbErr = s.store.WriteFile(ctx, storeAuth, st.path, *st.previous, st.pageMeta)
				slog.Info("ai: apply rollback restore", "path", st.path, "error", errString(rbErr))
			default:
				slog.Warn("ai: apply rollback skipped — no previous content", "path", st.path, "action", string(st.action))
			}
			if rbErr != nil {
				slog.Error("ai: apply rollback step failed",
					"path", st.path, "error", rbErr.Error(), "original_error", writeErr.Error())
			}
		}
		slog.Warn("ai: apply rollback finished", "failed_path", failedPath)
		return fmt.Errorf("write %q: %w", failedPath, writeErr)
	}

	for _, f := range plan.files {
		slog.Info("ai: apply file write", "path", f.path, "action", string(f.action))
		var err error
		if f.action == FileActionDelete || f.content == DraftDeleteMarker {
			err = s.store.DeleteFile(ctx, storeAuth, f.path)
		} else {
			err = s.store.WriteFile(ctx, storeAuth, f.path, f.content, f.pageMeta)
		}
		if err != nil {
			return nil, rollback(f.path, err)
		}
		steps = append(steps, appliedStep{
			path: f.path, previous: f.previous, action: f.action, pageMeta: f.pageMeta,
		})
		written = append(written, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: string(f.action), Content: f.content},
			previous:  f.previous,
		})
	}
	for _, f := range []*planFile{plan.layoutStart, plan.layoutEnd} {
		if f == nil {
			continue
		}
		slog.Info("ai: apply file write", "path", f.path, "action", string(f.action), "kind", "layout")
		if err := s.store.WriteFile(ctx, storeAuth, f.path, f.content, nil); err != nil {
			return nil, rollback(f.path, err)
		}
		steps = append(steps, appliedStep{
			path: f.path, previous: f.previous, action: f.action, isLayout: true,
		})
	}
	return written, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
		content := f.content
		action := string(f.action)
		if f.action == FileActionDelete {
			content = DraftDeleteMarker
			action = string(FileActionDelete)
		}
		staged = append(staged, writtenFile{
			generated: ai.GeneratedFile{Path: f.path, Action: action, Content: content},
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

// normalizePageRegistryStatus keeps AI-proposed page registration from
// unpublishing a live storefront route. Omitted/empty status and regenerating
// home must land as published — draft home is a live 404 (PageAccessGuard).
func normalizePageRegistryStatus(entry *themefs.PageEntry) string {
	if entry == nil {
		return "published"
	}
	status := strings.TrimSpace(strings.ToLower(entry.Status))
	page := strings.TrimSpace(strings.ToLower(entry.Page))
	slug := strings.TrimSpace(strings.ToLower(entry.Slug))
	typ := strings.TrimSpace(strings.ToLower(entry.Type))
	if page == "home" || slug == "home" || typ == "home" {
		return "published"
	}
	if status == "" || status == "published" {
		return "published"
	}
	return status
}

// pageRegistryWantPath derives the theme-relative liquid path for a registry
// entry (entry.Path is a route prefix, not a file path).
func pageRegistryWantPath(entry *themefs.PageEntry) string {
	if entry == nil {
		return ""
	}
	page := strings.TrimSpace(entry.Page)
	if page == "" {
		page = strings.TrimSpace(entry.Slug)
	}
	if page == "" {
		return ""
	}
	wantPath := "pages/" + page + ".liquid"
	if entry.Path == "/pages/auth" {
		wantPath = "pages/auth/" + page + ".liquid"
	}
	return wantPath
}

// attachPageRegistryEntry sets PageMeta on the matching proposed liquid file.
// FlowPOS upserts pages.json from that metadata — the canonical single-page
// registration path (prefer page_registry_entry over rewriting pages.json).
func attachPageRegistryEntry(plan *writePlan, entry *themefs.PageEntry) error {
	if plan == nil || entry == nil {
		return nil
	}
	wantPath := pageRegistryWantPath(entry)
	for i := range plan.files {
		if plan.files[i].path != wantPath {
			continue
		}
		plan.files[i].pageMeta = &themefs.PageMeta{
			Title:          entry.Title,
			Slug:           entry.Slug,
			Type:           entry.Type,
			Status:         normalizePageRegistryStatus(entry),
			SEOTitle:       entry.SEOTitle,
			SEODescription: entry.SEODescription,
			SEOKeywords:    entry.SEOKeywords,
			OGTitle:        entry.OGTitle,
			OGDescription:  entry.OGDescription,
			OGImagePath:    entry.OGImagePath,
		}
		return nil
	}
	return fmt.Errorf("register page: page_registry_entry (page %q, path %q) has no matching proposed file at %q", entry.Page, entry.Path, wantPath)
}
