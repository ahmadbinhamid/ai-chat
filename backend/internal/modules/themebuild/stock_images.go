package themebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"ai-chat/internal/stockimages"
)

// stockPhotos is every photo search_stock_images returned in one generation, by id: the only photos a placement may
// save, so the model can never make the platform download an arbitrary URL. Bounded by the turn's tool calls.
type stockPhotos struct {
	mu   sync.Mutex
	byID map[int]stockimages.Photo
}

func newStockPhotos() *stockPhotos { return &stockPhotos{byID: make(map[int]stockimages.Photo)} }

func (s *stockPhotos) record(photos []stockimages.Photo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range photos {
		s.byID[p.ID] = p
	}
}

func (s *stockPhotos) get(id int) (stockimages.Photo, bool) {
	if s == nil {
		return stockimages.Photo{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	return p, ok
}

// execSearchStockImages runs one stock search and records its results; a failed search tells the model to carry on
// with theme images.
func (s *Service) execSearchStockImages(ctx context.Context, tenantID uint64, found *stockPhotos, input json.RawMessage) (string, error) {
	in, err := stockimages.ParseInput(input)
	if err != nil {
		return "", err
	}
	photos, err := s.stock.Search(ctx, tenantID, in)
	if err != nil {
		slog.Warn("stock image search failed", "tenant_id", tenantID, "error", err)
		return "", fmt.Errorf("%w — continue with the theme's own images", err)
	}
	found.record(photos)
	if len(photos) == 0 {
		return `{"photos":[],"note":"No photos matched; try a broader query or use the theme's own images."}`, nil
	}
	out, err := json.Marshal(map[string]any{
		"photos": photos,
		"use":    `Save one with {"stock_image": <id>, "path": "images/<new-name>.jpg"} in use_attachments, then reference it with {{ 'images/<new-name>.jpg' | asset_url }}.`,
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// enableStockPlacements lets in's use_attachments save photos from found: downloaded within the theme-image size limit
// and stored as a stock_image attachment on the turn's user message, after which staging, preview and Apply treat it
// exactly like an attached image.
func (s *Service) enableStockPlacements(in GenerateInput, found *stockPhotos) {
	if in.imageCatalog == nil || in.UserMessageID == nil {
		return
	}
	messageID, tenantID := *in.UserMessageID, in.TenantID
	in.imageCatalog.stock = found
	in.imageCatalog.downloadStock = func(ctx context.Context, p stockimages.Photo) ([]byte, error) {
		return s.stock.Download(ctx, p, s.placedImageLimit())
	}
	in.imageCatalog.saveStock = func(ctx context.Context, filename, mediaType string, data []byte) (string, error) {
		return s.chats.AddStockImage(ctx, messageID, tenantID, filename, mediaType, data)
	}
}
