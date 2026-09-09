package handlers

import (
	"bytes"
	"net/http"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// AttachmentHandler serves one chat message attachment's raw bytes — an
// attached image or the single reference HTML file a merchant attached to
// a prompt (see the image/HTML-attachment features). This route exists
// because GET /chat's transcript is metadata-only (chat.MessageAttachment's
// Content is never serialized — see its own doc comment): the frontend
// renders an attached image as a real <img> thumbnail
// (AttachedFilesPreview.tsx), which needs a URL to fetch bytes from instead
// of the base64 field GET /chat used to inline directly.
type AttachmentHandler struct {
	builder *themebuild.Service
}

func NewAttachmentHandler(builder *themebuild.Service) *AttachmentHandler {
	return &AttachmentHandler{builder: builder}
}

// Get handles GET /chats/:chatId/messages/:messageId/attachments/:attachmentId.
func (h *AttachmentHandler) Get(c *gin.Context) {
	a, err := h.builder.GetAttachmentContent(c.Request.Context(), auth.TenantID(c),
		c.Param("chatId"), c.Param("messageId"), c.Param("attachmentId"))
	if err != nil {
		respondErr(c, err)
		return
	}

	// private — same reasoning as AssetHandler.Get: this route sits behind
	// auth and isn't safe for a shared/intermediate cache to store; the URL
	// alone carries no tenant, so a cache keyed on it could serve one
	// tenant's bytes to another. Unlike a theme asset (a mutable slot — see
	// AssetHandler.Get's own no-cache reasoning), an attachment id is
	// genuinely immutable once written: the row is never updated, only
	// inserted, so THIS URL will return THESE bytes forever. That earns a
	// long max-age + immutable instead of AssetHandler's no-cache — a
	// browser can skip revalidation entirely, not just make it cheap. The
	// ETag (a real content hash, not synthesized) stays anyway, for any
	// intermediary that revalidates regardless (a forced refresh, a cache
	// that doesn't honor immutable).
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Header("ETag", `"`+a.Checksum+`"`)
	// Content-Type set explicitly from the stored media_type, same as
	// AssetHandler.Get — http.ServeContent only sniffs/guesses one from the
	// filename when this header is still unset, and a.MediaType (recorded
	// at write time — see chat.buildAttachments) is authoritative.
	c.Header("Content-Type", a.MediaType)
	c.Header("Content-Disposition", `inline; filename="`+a.Filename+`"`)
	http.ServeContent(c.Writer, c.Request, a.Filename, time.Time{}, bytes.NewReader(a.Content))
}
