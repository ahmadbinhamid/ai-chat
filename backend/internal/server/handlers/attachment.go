package handlers

import (
	"bytes"
	"net/http"
	"time"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// AttachmentHandler serves one chat message attachment's raw bytes, since GET /chat's
// transcript is metadata-only and the frontend needs a URL to fetch bytes from.
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

	// private: URL carries no tenant, so a shared cache must not serve it across tenants.
	// Attachment rows never change, so immutable+long max-age lets browsers skip revalidation.
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.Header("ETag", `"`+a.Checksum+`"`)
	// a.MediaType is authoritative from write time; http.ServeContent would only guess from the filename.
	c.Header("Content-Type", a.MediaType)
	c.Header("Content-Disposition", `inline; filename="`+a.Filename+`"`)
	http.ServeContent(c.Writer, c.Request, a.Filename, time.Time{}, bytes.NewReader(a.Content))
}
