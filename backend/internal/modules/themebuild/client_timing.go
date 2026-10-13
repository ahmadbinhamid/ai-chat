package themebuild

import (
	"context"
)

// ClientTiming is the browser's report for one generation, each value ms after send; nil means not reported.
type ClientTiming struct {
	PostMs           *uint32
	WSConnectMs      *uint32
	FirstEventMs     *uint32
	FirstProgressMs  *uint32
	CompletedMs      *uint32
	PreviewVisibleMs *uint32
}

// RecordClientTiming stores the browser's timing for a generation of the caller's own chat; ErrNotFound otherwise.
func (s *Service) RecordClientTiming(ctx context.Context, tenantID uint64, chatID, generationID string, t ClientTiming) error {
	c, err := s.chats.GetChat(ctx, tenantID, chatID)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetGenerationByID(ctx, c.ID, generationID); err != nil {
		return err
	}
	return s.repo.UpsertClientTiming(ctx, generationID, c.ID, c.TenantID, t)
}
