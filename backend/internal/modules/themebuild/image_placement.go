package themebuild

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/imageplacement"
	"ai-chat/internal/modules/chat"
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
	Current   bool // attached to the message this turn answers
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
}

// newImageCatalog numbers every image attachment in priorMessages, which must be oldest-first.
func newImageCatalog(priorMessages []chat.Message, currentMessageID string, draft map[string]string,
	load func(ctx context.Context, attachmentID string) ([]byte, error)) *imageCatalog {
	c := &imageCatalog{draft: draft, load: load, loaded: make(map[string][]byte), maxBytes: imageplacement.DefaultMaxBytes}
	for _, m := range priorMessages {
		for _, a := range m.Attachments {
			if a.Kind != chat.AttachmentKindImage {
				continue
			}
			c.images = append(c.images, chatImage{
				Number: len(c.images) + 1, ID: a.ID, Filename: a.Filename, MediaType: a.MediaType,
				Current: m.ID == currentMessageID,
			})
		}
	}
	return c
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
	b.WriteString("Only place one (in use_attachments) when the merchant's own words ask you to use, place, add or put " +
		"it in the theme. An image sent to show a look or style (\"make it look like this\") is a reference, not a " +
		"placement. The platform copies the real file; you only name it and its path.\n")
	current := 0
	for _, img := range listed {
		where := "from an earlier message"
		if img.Current {
			current++
			where = fmt.Sprintf("image %d attached to the message above", current)
		}
		fmt.Fprintf(&b, "- Attached image %d: %s (%s, %s)\n", img.Number, where, img.Filename, img.MediaType)
	}
	b.WriteString("--- end of attached images ---")
	return b.String()
}

// findings checks every placement, as themecheck findings so the repair round fixes them. snap.Paths includes the draft.
func (c *imageCatalog) findings(ctx context.Context, placements []ai.AttachmentPlacement, snap themecheck.Snapshot) []themecheck.Finding {
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
		seen[p.Path] = true
		if msg != "" {
			out = append(out, themecheck.Finding{Path: p.Path, Rule: ruleIDUseAttachments, Severity: themecheck.SeverityError, Message: msg})
		}
	}
	return out
}

func (c *imageCatalog) checkOne(ctx context.Context, p ai.AttachmentPlacement, snap themecheck.Snapshot) string {
	if err := themefs.ValidatePathSafety(p.Path); err != nil {
		return err.Error()
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

// planFiles turns accepted placements into create rows whose content is the attachment reference, never the bytes.
func (c *imageCatalog) planFiles(ctx context.Context, store themefs.ThemeStore, storeAuth themefs.RequestAuth, placements []ai.AttachmentPlacement) ([]planFile, error) {
	files := make([]planFile, 0, len(placements))
	for _, p := range placements {
		img, ok := c.byNumber(p.Attachment)
		if !ok {
			return nil, fmt.Errorf("place attached image %d: no such attachment", p.Attachment)
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
			path: p.Path, action: FileActionCreate, content: imageplacement.Reference(img.ID),
			previous: previousPtr, attachmentID: img.ID,
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
