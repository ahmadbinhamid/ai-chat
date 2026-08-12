package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

// ErrRevertBlockedByRunningGeneration means the chat has a generation
// currently in progress — reverting concurrently with it writing files
// would race, so the caller should ask the merchant to wait and retry.
var ErrRevertBlockedByRunningGeneration = errors.New("a generation is currently in progress for this chat")

// RevertResult summarizes what RevertToMessage changed.
type RevertResult struct {
	// RestoredFiles were rewritten back to the content they had immediately
	// after the target turn.
	RestoredFiles []string `json:"restored_files"`
	// DeletedFiles didn't exist yet as of the target turn (a later turn
	// created them) — reverting means removing them entirely.
	DeletedFiles []string `json:"deleted_files"`
}

// RevertToMessage undoes every turn after messageID's — the exact meaning
// of "undo" now depends on whether messageID falls inside the chat's
// current, still-unapplied draft or before it, since generation no longer
// writes to FlowPOS immediately (see themebuild's package doc comment):
//
//   - messageID.ApplyStatus == pending: the target turn is itself part of
//     the current draft, nothing about it has ever reached FlowPOS. Revert
//     is a pure draft operation (revertWithinDraft) — discard every
//     'pending' message after it so the draft overlay (Repository.DraftFiles)
//     stops seeing their rows and falls back to whatever was there before
//     them. Zero FlowPOS calls: there is nothing on FlowPOS to undo yet.
//   - otherwise (applied / not_applicable): messageID is from before the
//     current draft — anything the draft itself has staged is irrelevant to
//     what's actually live on FlowPOS right now, so this still needs to
//     write (revertAppliedHistory), but computed ONLY from 'applied' rows,
//     never 'pending'/'discarded' ones, which were never written and would
//     otherwise pollute "what does the live theme currently look like".
//
// This is ai-chat's answer to "git-backed drafts and revert": themefs.Store
// is an HTTP client to flowpos-backend's theme-file API (see its own doc
// comment) — this process never has the theme's files on local disk, so
// there is no working directory here to run `git init`/`git commit`
// against. The chat_generated_files audit trail this service already
// writes on every turn (content + previous_content, per file, per turn) is
// the equivalent history that actually exists in ai-chat's own boundary.
//
// Content-only: a page's pages.json fields (title, SEO metadata, status)
// aren't tracked per-turn independently of file content, so those reflect
// the latest turn that set them even after a content revert — restoring
// them to a specific turn's values would need pages.json snapshotting this
// service doesn't do. Deleting a file does fully un-register its page,
// since flowpos-backend's own delete endpoint removes the pages.json entry
// too (see themefs.Store.DeleteFile).
func (s *Service) RevertToMessage(ctx context.Context, tenantID uint64, token, chatID, messageID string) (RevertResult, error) {
	if _, err := s.chats.GetChat(ctx, tenantID, chatID); err != nil {
		return RevertResult{}, err
	}
	target, err := s.chats.GetMessage(ctx, chatID, messageID)
	if err != nil {
		return RevertResult{}, err
	}

	if gen, err := s.repo.GetGeneration(ctx, chatID); err == nil && gen.Status == GenerationStatusRunning {
		return RevertResult{}, ErrRevertBlockedByRunningGeneration
	}

	if target.ApplyStatus == chat.ApplyStatusPending {
		return s.revertWithinDraft(ctx, chatID, target)
	}
	return s.revertAppliedHistory(ctx, tenantID, token, chatID, target)
}

// revertWithinDraft discards every still-pending message created after
// target — see RevertToMessage's doc comment. No themeLocks use here
// either: nothing is read-modify-written against the real theme, only a
// chat_messages UPDATE, which needs no more coordination than any other
// single-statement write already gets from the database itself.
func (s *Service) revertWithinDraft(ctx context.Context, chatID string, target chat.Message) (RevertResult, error) {
	discardedPaths, err := s.repo.DiscardMessagesAfter(ctx, chatID, target.CreatedAt)
	if err != nil {
		return RevertResult{}, fmt.Errorf("revert within draft: %w", err)
	}
	sort.Strings(discardedPaths)
	// Framed as "restored" even though nothing was written anywhere: from
	// the merchant's perspective, the draft preview will now render as it
	// did right after the target turn (whatever an earlier draft row, or
	// the last-applied theme, has for these paths) — RevertResult has no
	// separate "nothing to do here" shape, and DeletedFiles specifically
	// means "didn't exist as of the target turn," which discarding a later
	// pending row doesn't establish either way without checking prior
	// draft state, so RestoredFiles is the closer fit of the two.
	return RevertResult{RestoredFiles: discardedPaths}, nil
}

// revertAppliedHistory is RevertToMessage's original logic, scoped to only
// 'applied' rows (see RevertToMessage's doc comment on why 'pending'/
// 'discarded' rows must never enter this computation): restore every path
// APPLIED after target back to its last-applied-at-or-before-target state,
// or delete it if it didn't exist applied yet then.
func (s *Service) revertAppliedHistory(ctx context.Context, tenantID uint64, token, chatID string, target chat.Message) (RevertResult, error) {
	files, err := s.repo.ListAppliedFilesByChat(ctx, chatID)
	if err != nil {
		return RevertResult{}, err
	}

	// latestAtOrBefore holds, per path, the last APPLIED audit row at or
	// before the target turn — absent means the path wasn't applied yet
	// then. touchedAfter holds every path any LATER applied turn touched.
	//
	// Ordering is by created_at, a DATETIME column with only second-level
	// precision (matching chat_messages/chat_generated_files as they exist
	// today) — two turns landing in the same wall-clock second would tie
	// here. In practice a turn takes seconds to minutes (it calls Claude),
	// so this isn't reachable through normal use; flagged rather than
	// silently assumed, since revert is exactly the kind of operation
	// where a wrong answer is worse than a slow one.
	latestAtOrBefore := make(map[string]GeneratedFile)
	touchedAfter := make(map[string]bool)
	for _, f := range files {
		if !f.CreatedAt.After(target.CreatedAt) {
			latestAtOrBefore[f.FilePath] = f
		} else {
			touchedAfter[f.FilePath] = true
		}
	}

	// Serialized against a concurrent generation the same way doGenerate's
	// own staging section is (see themeLocks) — keyed by chatID rather than
	// themeSlug, since a chat carries no theme_slug of its own to lock on;
	// this still prevents two reverts of the same chat from racing.
	unlock, err := s.themeLocks.Lock(ctx, chatID)
	if err != nil {
		return RevertResult{}, fmt.Errorf("revert applied history: %w", err)
	}
	defer unlock()

	storeAuth := themefs.RequestAuth{Token: token, TenantID: tenantID}
	var result RevertResult
	for path := range touchedAfter {
		if before, ok := latestAtOrBefore[path]; ok {
			if err := s.store.WriteFile(ctx, storeAuth, path, before.Content, nil); err != nil {
				return result, fmt.Errorf("restore %q: %w", path, err)
			}
			result.RestoredFiles = append(result.RestoredFiles, path)
			continue
		}
		if err := s.store.DeleteFile(ctx, storeAuth, path); err != nil {
			return result, fmt.Errorf("delete %q: %w", path, err)
		}
		result.DeletedFiles = append(result.DeletedFiles, path)
	}

	sort.Strings(result.RestoredFiles)
	sort.Strings(result.DeletedFiles)
	return result, nil
}
