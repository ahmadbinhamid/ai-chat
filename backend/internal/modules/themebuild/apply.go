package themebuild

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ai-chat/internal/themefs"
)

// ErrApplyBlockedByRunningGeneration mirrors
// ErrRevertBlockedByRunningGeneration: applying while a generation is
// running or queued would race that generation's own staging writes
// against ApplyDraft's read of PendingFiles — the generation could stage a
// new pending row for a path ApplyDraft has already decided isn't part of
// this apply, or ApplyDraft's commit could run mid-generation and leave the
// generation's own draft reads observing a half-applied theme. Simplest
// safe rule: nothing pending may be running or queued.
var ErrApplyBlockedByRunningGeneration = errors.New("a generation is currently running or queued for this chat")

// ErrNoPendingChanges means DraftSummary reports nothing pending — Apply
// and Discard both reject this as a no-op rather than silently succeeding
// at nothing.
var ErrNoPendingChanges = errors.New("this chat has no pending changes to apply")

// ApplyResult summarizes what ApplyDraft wrote. AppliedPaths deliberately
// excludes layout splices (layout-start.liquid/layout-end.liquid), the same
// way DraftSummaryResult.FilePaths does — a merchant sees both (the "N
// unsaved changes" summary before applying, this list after), and the two
// must agree on what counts as "a file" or the count visibly changes
// between the two without anything the merchant did explaining why.
type ApplyResult struct {
	AppliedPaths []string `json:"applied_paths"`
}

// DiscardResult summarizes what DiscardDraft threw away.
type DiscardResult struct {
	DiscardedPaths []string `json:"discarded_paths"`
	DiscardedTurns int      `json:"discarded_turns"`
}

// DraftSummaryResult is GET /chat's pending_changes field — see
// Service.DraftSummary.
type DraftSummaryResult struct {
	HasChanges   bool     `json:"has_changes"`
	FilePaths    []string `json:"file_paths"`
	MessageCount int      `json:"message_count"`
}

// DraftSummary reports whether chatID has an unapplied draft, and if so,
// which paths and how many turns contributed to it — GET /chat's
// pending_changes field. kind='layout' rows are excluded from FilePaths: a
// layout splice isn't something the merchant asked for or should see
// listed as "a file" (see GeneratedFileKind's doc comment) — it's still
// applied along with everything else, just not surfaced here.
func (s *Service) DraftSummary(ctx context.Context, chatID string) (DraftSummaryResult, error) {
	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return DraftSummaryResult{}, err
	}
	if len(files) == 0 {
		return DraftSummaryResult{FilePaths: []string{}}, nil
	}

	seenPaths := make(map[string]bool)
	seenMessages := make(map[string]bool)
	var paths []string
	for _, f := range files {
		seenMessages[f.MessageID] = true
		if f.Kind == GeneratedFileKindLayout {
			continue
		}
		if seenPaths[f.FilePath] {
			continue
		}
		seenPaths[f.FilePath] = true
		paths = append(paths, f.FilePath)
	}
	if paths == nil {
		paths = []string{}
	}
	return DraftSummaryResult{HasChanges: true, FilePaths: paths, MessageCount: len(seenMessages)}, nil
}

// ApplyDraft writes chatID's entire pending draft to the real theme and
// marks every pending message applied. Uses the BASE store (s.store), not
// an overlay — applying IS the write the overlay exists to defer, so this
// is the one place in the service that's deliberately not draft-aware on
// its read side: PendingFiles (not an overlay ReadFile) is the source of
// truth for what to write.
//
// On partial failure (commitWritePlan errors after writing some paths but
// not all — flowpos-backend is a sequence of independent HTTP calls, not a
// transaction), nothing is marked applied: every pending message stays
// 'pending', so the merchant sees the same "N unsaved changes" state and
// can retry. A retryable partial beats a draft falsely marked applied,
// which would strand the failed path's content nowhere (not in the theme,
// not visibly still-pending) with no way to get it applied short of
// re-prompting the model. The returned error names the failed path (via
// commitWritePlan's own %q-wrapped error) so the merchant/logs know what
// to look at if retrying doesn't help.
func (s *Service) ApplyDraft(ctx context.Context, tenantID uint64, token, chatID, themeSlug string) (ApplyResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return ApplyResult{}, err
	}

	pending, err := s.repo.ListPending(ctx, chatID)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(pending) > 0 {
		return ApplyResult{}, ErrApplyBlockedByRunningGeneration
	}

	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(files) == 0 {
		return ApplyResult{}, ErrNoPendingChanges
	}

	unlock, err := s.themeLocks.Lock(ctx, themeSlug)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply draft: %w", err)
	}
	defer unlock()

	plan := pendingFilesToPlan(files)

	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	written, err := s.commitWritePlan(ctx, storeAuth, plan)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply draft: %w", err)
	}

	if err := s.repo.MarkMessagesApplied(ctx, chatID, time.Now().UTC()); err != nil {
		return ApplyResult{}, fmt.Errorf("mark draft applied: %w", err)
	}

	// written only ever contains plan.files (commitWritePlan never appends
	// layoutStart/layoutEnd to it — see its own doc comment), so this is
	// naturally already scoped to non-layout paths; no separate filter
	// needed the way DraftSummary needs one against PendingFiles' raw kind
	// column. Deliberately NOT also appending plan.layoutStart/layoutEnd's
	// paths here (a prior version of this code did): AppliedPaths and
	// DraftSummary.FilePaths must stay consistent with each other, since a
	// merchant sees both — DraftSummary already excludes layout splices
	// (GeneratedFileKindLayout, "not something the merchant asked for or
	// should see listed as 'a file'" — see its own doc comment), and
	// AppliedPaths reporting more paths than DraftSummary staged would show
	// as "2 unsaved changes" becoming "applied 4 files" for the same draft.
	paths := make([]string, 0, len(written))
	for _, w := range written {
		paths = append(paths, w.generated.Path)
	}
	return ApplyResult{AppliedPaths: paths}, nil
}

// DraftFiles returns chatID's effective file map — the real (last-applied)
// theme overlaid with this chat's pending draft, exactly what
// themefs.OverlayStore/LoadThemeFiles would give a generation running
// right now. This is what the frontend's LiquidJS preview renders: after
// verifying chat ownership, it's the same "base + overlay" merge
// doGenerate itself reads from, just exposed read-only over HTTP (GET
// /chats/:chatId/draft) instead of only ever being built inside a
// generation call.
func (s *Service) DraftFiles(ctx context.Context, tenantID uint64, token, chatID string) (map[string]string, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return nil, err
	}
	draft, err := s.repo.DraftFiles(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("load draft overlay: %w", err)
	}
	store := themefs.NewOverlayStore(s.store, draft)
	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	return s.LoadThemeFiles(ctx, store, storeAuth, true)
}

// pendingFilesToPlan folds PendingFiles' rows into a writePlan —
// GeneratedFileKindProposed rows become plan.files (restoring PageMeta
// from the persisted column, since the model's own PageRegistryEntry no
// longer exists by the time Apply runs — see the 20260813000001
// migration's doc comment), GeneratedFileKindLayout rows become
// plan.layoutStart/layoutEnd by path. Only the latest row per path is
// kept, same last-write-wins rule DraftFiles applies, since PendingFiles
// already returns rows oldest-first.
func pendingFilesToPlan(files []GeneratedFile) writePlan {
	var plan writePlan
	proposedByPath := make(map[string]int) // path -> index into plan.files

	for _, f := range files {
		switch f.Kind {
		case GeneratedFileKindLayout:
			pf := &planFile{path: f.FilePath, action: f.Action, content: f.Content}
			switch f.FilePath {
			case pathLayoutStart:
				plan.layoutStart = pf
			case pathLayoutEnd:
				plan.layoutEnd = pf
			}
		default:
			pf := planFile{path: f.FilePath, action: f.Action, content: f.Content, previous: f.PreviousContent, pageMeta: f.PageMeta}
			if idx, ok := proposedByPath[f.FilePath]; ok {
				// A later turn editing an already-staged page carries no
				// PageRegistryEntry (the model only sends one when it's
				// introducing the page), so its pageMeta is nil — see
				// buildWritePlan. Replacing wholesale would drop the
				// registration the earlier turn staged and write the file
				// without ever registering the route. Keep whichever row
				// actually has it; a newer turn's own non-nil pageMeta
				// (re-registering, e.g. changing the slug) still wins.
				if pf.pageMeta == nil {
					pf.pageMeta = plan.files[idx].pageMeta
				}
				// Same reasoning for the action: a path created earlier in
				// this draft and then edited by a later turn is still a
				// create relative to the last-applied theme, and the audit
				// row should say so. (WriteFile itself doesn't branch on
				// action, so this only affects what the trail means, not
				// what gets written.)
				if plan.files[idx].action == FileActionCreate {
					pf.action = FileActionCreate
				}
				// previous is left as the newest row's value, unlike the
				// two fields above — it's the audit trail's "what this
				// turn replaced", and ApplyDraft never re-persists audit
				// rows, so nothing downstream reads it for anything but
				// this newest write.
				plan.files[idx] = pf
			} else {
				proposedByPath[f.FilePath] = len(plan.files)
				plan.files = append(plan.files, pf)
			}
		}
	}
	return plan
}

// DiscardDraft throws away chatID's entire pending draft — makes zero
// FlowPOS calls (see chat.ApplyStatusDiscarded's doc comment: messages
// stay in the transcript, marked discarded, never deleted). Blocked by a
// running/queued generation for the same reason ApplyDraft is: a
// generation mid-flight could stage a new pending row concurrently with
// this marking everything discarded, silently discarding work the
// merchant never saw finish.
func (s *Service) DiscardDraft(ctx context.Context, tenantID uint64, chatID string) (DiscardResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return DiscardResult{}, err
	}

	pending, err := s.repo.ListPending(ctx, chatID)
	if err != nil {
		return DiscardResult{}, err
	}
	if len(pending) > 0 {
		return DiscardResult{}, ErrApplyBlockedByRunningGeneration
	}

	files, err := s.repo.PendingFiles(ctx, chatID)
	if err != nil {
		return DiscardResult{}, err
	}
	if len(files) == 0 {
		return DiscardResult{}, ErrNoPendingChanges
	}

	if err := s.repo.MarkMessagesDiscarded(ctx, chatID); err != nil {
		return DiscardResult{}, err
	}

	seenPaths := make(map[string]bool)
	seenMessages := make(map[string]bool)
	var paths []string
	for _, f := range files {
		seenMessages[f.MessageID] = true
		if !seenPaths[f.FilePath] {
			seenPaths[f.FilePath] = true
			paths = append(paths, f.FilePath)
		}
	}
	return DiscardResult{DiscardedPaths: paths, DiscardedTurns: len(seenMessages)}, nil
}
