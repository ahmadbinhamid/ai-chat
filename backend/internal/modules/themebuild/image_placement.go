package themebuild

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/imageplacement"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/stockimages"
	"ai-chat/internal/themecheck"
	"ai-chat/internal/themefs"
)

const ruleIDUseAttachments = "use-attachments"

// maxListedImages bounds the prompt's image list; numbering still counts every image in the chat.
const maxListedImages = 20

// chatImage is one image attachment in the chat. Number is its 1-based position across every turn, oldest first, so
// "Attached image 2" means the same image on every later turn.
type chatImage struct {
	Number    int
	ID        string
	Filename  string
	MediaType string
	// MessagesAgo counts the merchant's messages since this one was sent; 0 is the message this turn answers.
	MessagesAgo int
	// Width and Height are 0 when unknown; Placed is nil when the image isn't in the theme or draft.
	Width, Height int
	Placed        *PlacedImage
}

// imageCatalog resolves use_attachments numbers for one generation. Not safe for concurrent use: only
// checkAndRepair and staging read it, one after the other.
type imageCatalog struct {
	images []chatImage
	draft  map[string]string
	load   func(ctx context.Context, attachmentID string) ([]byte, error)
	loaded map[string][]byte
	// maxBytes is checked here, at staging, so an oversized image goes to the repair round and never fails at Apply.
	maxBytes int
	// stock, downloadStock and saveStock are set only on a turn offered search_stock_images (enableStockPlacements).
	stock         *stockPhotos
	downloadStock func(ctx context.Context, p stockimages.Photo) ([]byte, error)
	saveStock     func(ctx context.Context, filename, mediaType string, data []byte) (string, error)
}

// newImageCatalog numbers every image attachment in priorMessages, which must be oldest-first.
func newImageCatalog(priorMessages []chat.Message, currentMessageID string, draft map[string]string,
	load func(ctx context.Context, attachmentID string) ([]byte, error)) *imageCatalog {
	c := &imageCatalog{draft: draft, load: load, loaded: make(map[string][]byte), maxBytes: imageplacement.DefaultMaxBytes}
	// Position among the merchant's own messages; without the current message, the latest one counts as 1 ago.
	userIndex := make(map[string]int)
	currentIndex := 0
	for _, m := range priorMessages {
		if m.Role != chat.RoleUser {
			continue
		}
		userIndex[m.ID] = currentIndex
		if m.ID == currentMessageID {
			break
		}
		currentIndex++
	}
	for _, m := range priorMessages {
		idx, isUser := userIndex[m.ID]
		if !isUser {
			continue
		}
		for _, a := range m.Attachments {
			if a.Kind != chat.AttachmentKindImage {
				continue
			}
			c.images = append(c.images, chatImage{
				Number: len(c.images) + 1, ID: a.ID, Filename: a.Filename, MediaType: a.MediaType,
				MessagesAgo: currentIndex - idx,
			})
		}
	}
	return c
}

// describe adds each image's dimensions (from its first bytes) and where it's already placed; either map may be nil.
func (c *imageCatalog) describe(heads map[string][]byte, placed map[string]PlacedImage) {
	for i := range c.images {
		img := &c.images[i]
		if w, h, ok := imageplacement.Dimensions(heads[img.ID]); ok {
			img.Width, img.Height = w, h
		}
		if p, ok := placed[img.ID]; ok {
			img.Placed = &p
		}
	}
}

func (c *imageCatalog) byNumber(n int) (chatImage, bool) {
	if c == nil || n < 1 || n > len(c.images) {
		return chatImage{}, false
	}
	return c.images[n-1], true
}

func (c *imageCatalog) bytes(ctx context.Context, id string) ([]byte, error) {
	if data, ok := c.loaded[id]; ok {
		return data, nil
	}
	data, err := c.load(ctx, id)
	if err != nil {
		return nil, err
	}
	c.loaded[id] = data
	return data, nil
}

// promptBlock lists the chat's images for use_attachments; "" when the chat has none. It goes in the user message so
// the cached system prompt stays byte-identical.
func (c *imageCatalog) promptBlock() string {
	if c == nil || len(c.images) == 0 {
		return ""
	}
	listed := c.images
	if len(listed) > maxListedImages {
		listed = listed[len(listed)-maxListedImages:]
	}
	var b strings.Builder
	b.WriteString("--- Images the merchant has attached in this chat ---\n")
	// Said next to the list, not only in the spec: the model follows a note at the decision point over a distant rule.
	b.WriteString("Images attached to this message are what \"this image\" and \"it\" mean. If the merchant asks to " +
		"use an image, use it whatever it shows — a screenshot or graphic is as valid as a photo. Never refuse or " +
		"second-guess it.\n")
	b.WriteString("Place one (in use_attachments) only when the merchant's own words ask you to use, place, add or put " +
		"it in the theme — an image sent to show a look or style (\"make it look like this\") is a reference, not a " +
		"placement. The platform copies the real file; you only name it and its path, and a file must use it.\n")
	newest := c.images[len(c.images)-1].Number
	for _, img := range listed {
		fmt.Fprintf(&b, "- Attached image %d: %s\n", img.Number, img.label(img.Number == newest))
	}
	b.WriteString("--- end of attached images ---")
	return b.String()
}

// label is one image's line in the list: when it was sent, its name and size, and whether it's already placed.
func (img chatImage) label(newest bool) string {
	var when string
	switch img.MessagesAgo {
	case 0:
		when = "attached to this message"
	case 1:
		when = "sent 1 message ago"
	default:
		when = fmt.Sprintf("sent %d messages ago", img.MessagesAgo)
	}
	if newest {
		when += " (the newest image)"
	}
	parts := []string{when, img.Filename}
	if img.Width > 0 && img.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d px", img.Width, img.Height))
	}
	switch {
	case img.Placed == nil:
		parts = append(parts, "not placed yet")
	case img.Placed.Live:
		parts = append(parts, "already placed at "+img.Placed.Path+" (live in the theme)")
	default:
		parts = append(parts, "already placed at "+img.Placed.Path+" (staged — goes live when the merchant applies)")
	}
	return strings.Join(parts, " — ")
}

// findings checks every placement, as themecheck findings so the repair round fixes them. snap.Paths includes the draft;
// proposalTexts are the proposed files' contents, which with the draft's must use each placed image.
func (c *imageCatalog) findings(ctx context.Context, placements []ai.AttachmentPlacement, snap themecheck.Snapshot, proposalTexts []string) []themecheck.Finding {
	var out []themecheck.Finding
	seen := make(map[string]bool, len(placements))
	for _, p := range placements {
		var msg string
		switch {
		case seen[p.Path]:
			msg = fmt.Sprintf("%s is placed more than once in use_attachments — give each image its own path", p.Path)
		default:
			msg = c.checkOne(ctx, p, snap)
		}
		// Apply drops an image nothing uses, so a placement that stays unused would read as a fake "saved".
		if msg == "" && !imageplacement.IsReferenced(p.Path, c.referenceTexts(proposalTexts)) {
			msg = fmt.Sprintf("You placed %s but no file uses it. Either reference it where the merchant asked, or remove "+
				"the placement and tell them images are only added to the theme when a page uses them.", p.Path)
		}
		seen[p.Path] = true
		if msg != "" {
			out = append(out, themecheck.Finding{Path: p.Path, Rule: ruleIDUseAttachments, Severity: themecheck.SeverityError, Message: msg})
		}
	}
	return out
}

// referenceTexts is every file a placed image could be used from: the proposal plus the draft's other files.
func (c *imageCatalog) referenceTexts(proposalTexts []string) []string {
	texts := append([]string(nil), proposalTexts...)
	for _, content := range c.draft {
		if _, isRef := imageplacement.ParseReference(content); !isRef {
			texts = append(texts, content)
		}
	}
	return texts
}

func (c *imageCatalog) checkOne(ctx context.Context, p ai.AttachmentPlacement, snap themecheck.Snapshot) string {
	if err := themefs.ValidatePathSafety(p.Path); err != nil {
		return err.Error()
	}
	if p.StockImage != 0 {
		return c.checkStock(ctx, p, snap)
	}
	placement := imageplacement.Placement{Attachment: p.Attachment, Path: p.Path}
	if err := imageplacement.CheckPath(p.Path); err != nil {
		return err.Error()
	}
	img, found := c.byNumber(p.Attachment)
	var data []byte
	if found {
		var err error
		if data, err = c.bytes(ctx, img.ID); err != nil {
			slog.Warn("use_attachments: attachment unreadable", "attachment_id", img.ID, "error", err)
			found = false
		}
	}
	// The same image already staged at this path by an earlier turn isn't an overwrite; staging drops it as unchanged.
	taken := snap.HasPath(p.Path) && (!found || c.draft[p.Path] != imageplacement.Reference(img.ID))
	if err := imageplacement.Check(placement, data, found, taken, c.maxBytes); err != nil {
		return err.Error()
	}
	return ""
}

// checkStock checks a stock_image placement: only a photo this turn's search returned, downloaded once, then the same
// path, size and type rules as an attached image.
func (c *imageCatalog) checkStock(ctx context.Context, p ai.AttachmentPlacement, snap themecheck.Snapshot) string {
	if p.Attachment != 0 {
		return fmt.Sprintf("%s sets both attachment and stock_image — set only one", p.Path)
	}
	if c.downloadStock == nil {
		return fmt.Sprintf("%s: no stock photos are available this turn — remove the stock_image placement", p.Path)
	}
	photo, found := c.stock.get(p.StockImage)
	var data []byte
	if found {
		var err error
		if data, err = c.stockBytes(ctx, photo); err != nil {
			slog.Warn("use_attachments: stock photo download failed", "stock_image", p.StockImage, "error", err)
			if errors.Is(err, stockimages.ErrTooLarge) {
				return fmt.Sprintf("stock photo %d is over the %s limit for theme images — choose another photo",
					p.StockImage, imageplacement.FormatSize(c.maxBytes))
			}
			return fmt.Sprintf("stock photo %d couldn't be downloaded — choose another photo or use the theme's own images", p.StockImage)
		}
	}
	if err := imageplacement.CheckStock(p.StockImage, p.Path, data, found, snap.HasPath(p.Path), c.maxBytes); err != nil {
		return err.Error()
	}
	return ""
}

func (c *imageCatalog) stockBytes(ctx context.Context, photo stockimages.Photo) ([]byte, error) {
	key := fmt.Sprintf("stock:%d", photo.ID)
	if data, ok := c.loaded[key]; ok {
		return data, nil
	}
	data, err := c.downloadStock(ctx, photo)
	if err != nil {
		return nil, err
	}
	c.loaded[key] = data
	return data, nil
}

// saveStockPhoto stores an accepted stock placement's bytes as an attachment, so it stages like an attached image.
func (c *imageCatalog) saveStockPhoto(ctx context.Context, p ai.AttachmentPlacement) (string, error) {
	photo, ok := c.stock.get(p.StockImage)
	if !ok || c.saveStock == nil {
		return "", fmt.Errorf("place stock photo %d: not returned this turn", p.StockImage)
	}
	data, err := c.stockBytes(ctx, photo)
	if err != nil {
		return "", fmt.Errorf("place stock photo %d: %w", p.StockImage, err)
	}
	format, ok := imageplacement.Sniff(data)
	if !ok {
		return "", fmt.Errorf("place stock photo %d: not a PNG, JPEG or WebP image", p.StockImage)
	}
	id, err := c.saveStock(ctx, path.Base(p.Path), format.MediaType, data)
	if err != nil {
		return "", fmt.Errorf("save stock photo %d: %w", p.StockImage, err)
	}
	return id, nil
}

// planFiles turns accepted placements into create rows whose content is the attachment reference, never the bytes.
func (c *imageCatalog) planFiles(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, placements []ai.AttachmentPlacement) ([]planFile, error) {
	files := make([]planFile, 0, len(placements))
	for _, p := range placements {
		var attachmentID string
		if p.StockImage != 0 {
			id, err := c.saveStockPhoto(ctx, p)
			if err != nil {
				return nil, err
			}
			attachmentID = id
		} else {
			img, ok := c.byNumber(p.Attachment)
			if !ok {
				return nil, fmt.Errorf("place attached image %d: no such attachment", p.Attachment)
			}
			attachmentID = img.ID
		}
		previous, err := store.ReadFile(ctx, storeAuth, p.Path)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", p.Path, err)
		}
		var previousPtr *string
		if previous != "" {
			previousPtr = &previous
		}
		files = append(files, planFile{
			path: p.Path, action: FileActionCreate, content: imageplacement.Reference(attachmentID),
			previous: previousPtr, attachmentID: attachmentID,
		})
	}
	return files, nil
}

// errPlacedImageInvalid means a staged attachment no longer passes the checks it passed at staging time.
var errPlacedImageInvalid = errors.New("placed image is not a PNG, JPEG or WebP within the size limit")

// loadPlacedImage reads a placed attachment's bytes, re-checked by magic bytes so Apply never uploads anything else.
func (s *Service) loadPlacedImage(ctx context.Context, chatID, attachmentID string) ([]byte, imageplacement.Format, error) {
	a, err := s.chats.GetChatImageAttachment(ctx, chatID, attachmentID)
	if err != nil {
		return nil, imageplacement.Format{}, fmt.Errorf("load attachment %s: %w", attachmentID, err)
	}
	format, ok := imageplacement.Sniff(a.Content)
	if !ok || len(a.Content) > s.placedImageLimit() {
		return nil, imageplacement.Format{}, fmt.Errorf("attachment %s: %w", attachmentID, errPlacedImageInvalid)
	}
	return a.Content, format, nil
}

// uploadPlacedImage copies a placed attachment into the live theme.
func (s *Service) uploadPlacedImage(ctx context.Context, storeAuth themefs.RequestAuth, chatID, attachmentID, path string) error {
	data, format, err := s.loadPlacedImage(ctx, chatID, attachmentID)
	if err != nil {
		return err
	}
	if err := s.store.UploadFile(ctx, storeAuth, path, data, format.MediaType); err != nil {
		return fmt.Errorf("upload %q: %w", path, err)
	}
	return nil
}

// ErrImageUploadFailed is the merchant-facing outcome of an Apply that stopped because a placed image didn't upload.
var ErrImageUploadFailed = errors.New("an image couldn't be uploaded to your theme, so nothing was applied — your " +
	"changes are still waiting; please try Apply again in a moment")

// imageUploadError keeps the raw cause and any image the rollback couldn't remove for the logs; the merchant only
// ever sees ErrImageUploadFailed.
type imageUploadError struct {
	path     string
	cause    error
	leftover []string
}

func (e *imageUploadError) Error() string {
	msg := fmt.Sprintf("upload placed image %q: %v", e.path, e.cause)
	if len(e.leftover) > 0 {
		msg += fmt.Sprintf(" (could not remove already-uploaded %s)", strings.Join(e.leftover, ", "))
	}
	return msg
}

func (e *imageUploadError) Unwrap() []error { return []error{ErrImageUploadFailed, e.cause} }

// rollBackUploads removes the images this Apply already uploaded, since no file that references them will be written.
func (s *Service) rollBackUploads(ctx context.Context, storeAuth themefs.RequestAuth, chatID, failedPath string, cause error, uploaded []string) error {
	uploadErr := &imageUploadError{path: failedPath, cause: cause}
	for _, path := range uploaded {
		if err := s.store.DeleteFile(ctx, storeAuth, path); err != nil {
			slog.Error("apply: could not remove an image uploaded before a later upload failed",
				"chat_id", chatID, "path", path, "error", err)
			uploadErr.leftover = append(uploadErr.leftover, path)
		}
	}
	slog.Error("apply: placed image upload failed; no theme files written, draft left pending",
		"chat_id", chatID, "path", failedPath, "rolled_back", len(uploaded)-len(uploadErr.leftover),
		"leftover", uploadErr.leftover, "error", cause)
	return uploadErr
}

// ReadPreviewAssetBytes serves the preview's theme assets: an image still staged in this tenant's draft comes from its
// attachment, since FlowPOS doesn't have it until Apply; anything else comes from the live theme.
func (s *Service) ReadPreviewAssetBytes(ctx context.Context, storeAuth themefs.RequestAuth, relPath string) ([]byte, error) {
	// Only a path a placement could occupy can be staged; s.repo is nil in tests without a database.
	if s.repo == nil || imageplacement.CheckPath(relPath) != nil {
		return s.ReadThemeAssetBytes(ctx, storeAuth, relPath)
	}
	c, err := s.chats.GetChatForTenant(ctx, storeAuth.TenantID, ChatType)
	switch {
	case errors.Is(err, chat.ErrNotFound):
		return s.ReadThemeAssetBytes(ctx, storeAuth, relPath)
	case err != nil:
		return nil, err
	}
	attachmentID, staged, err := s.repo.PendingAttachmentAt(ctx, c.ID, relPath)
	if err != nil {
		return nil, err
	}
	if !staged {
		return s.ReadThemeAssetBytes(ctx, storeAuth, relPath)
	}
	data, _, err := s.loadPlacedImage(ctx, c.ID, attachmentID)
	return data, err
}

// describeImages labels the catalog's images with dimensions and placement. Fails open: the labels help the model
// pick the right image but are never worth failing a generation over.
func (s *Service) describeImages(ctx context.Context, chatID string, c *imageCatalog) {
	if len(c.images) == 0 {
		return
	}
	heads, err := s.chats.ListChatImageHeads(ctx, chatID, imageplacement.HeadBytes)
	if err != nil {
		slog.Warn("load image dimensions failed; listing images without them", "chat_id", chatID, "error", err)
	}
	placed, err := s.repo.PlacedAttachments(ctx, chatID)
	if err != nil {
		slog.Warn("load placed images failed; listing images without placement status", "chat_id", chatID, "error", err)
	}
	c.describe(heads, placed)
}
