package themebuild

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

// ErrRevertBlockedByRunningGeneration means a generation is in progress; reverting concurrently
// with it writing files would race.
var ErrRevertBlockedByRunningGeneration = errors.New("a generation is currently in progress for this chat")

// RevertResult summarizes what RevertToMessage changed.
type RevertResult struct {
	// RestoredFiles were rewritten back to their content immediately after the target turn.
	RestoredFiles []string `json:"restored_files"`
	// DeletedFiles didn't exist yet as of the target turn, so reverting removes them entirely.
	DeletedFiles []string `json:"deleted_files"`
}

// RevertToMessage undoes every turn after messageID's: a pure draft op if messageID is still
// pending, else restores live FlowPOS state from 'applied' rows only. Content-only.
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

// revertWithinDraft discards every still-pending message after target. No themeLocks here:
// only a chat_messages UPDATE, which needs no more coordination than the DB already gives it.
func (s *Service) revertWithinDraft(ctx context.Context, chatID string, target chat.Message) (RevertResult, error) {
	discardedPaths, err := s.repo.DiscardMessagesAfter(ctx, chatID, target.CreatedAt)
	if err != nil {
		return RevertResult{}, fmt.Errorf("revert within draft: %w", err)
	}
	sort.Strings(discardedPaths)
	// Framed as "restored" though nothing was written: the draft preview will render as it did
	// right after the target turn, and RevertResult has no "nothing written" shape.
	return RevertResult{RestoredFiles: discardedPaths}, nil
}

// revertAppliedHistory restores every path applied after target to its last-applied-at-or-before
// state, or deletes it if not yet applied then. Scoped to 'applied' rows only.
func (s *Service) revertAppliedHistory(ctx context.Context, tenantID uint64, token, chatID string, target chat.Message) (RevertResult, error) {
	files, err := s.repo.ListAppliedFilesByChat(ctx, chatID)
	if err != nil {
		return RevertResult{}, err
	}

	// latestAtOrBefore: last applied row at/before target per path (absent = not applied yet).
	// touchedAfter: every path a later applied turn touched. DATETIME(6) keeps same-second turns ordered.
	latestAtOrBefore := make(map[string]GeneratedFile)
	touchedAfter := make(map[string]bool)
	for _, f := range files {
		if !f.CreatedAt.After(target.CreatedAt) {
			latestAtOrBefore[f.FilePath] = f
		} else {
			touchedAfter[f.FilePath] = true
		}
	}

	// Keyed by chatID, not themeSlug (a chat has no slug of its own), but still prevents two
	// reverts of the same chat from racing.
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
