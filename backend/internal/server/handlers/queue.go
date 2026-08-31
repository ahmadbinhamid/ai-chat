package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// QueueHandler exposes cancelling a prompt, queued or already running —
// see themebuild.Service.CancelQueuedGeneration's doc comment for how the
// two cases differ (a queued prompt stops synchronously here; a running
// one stops asynchronously, once its own goroutine notices).
type QueueHandler struct {
	builder *themebuild.Service
}

func NewQueueHandler(builder *themebuild.Service) *QueueHandler {
	return &QueueHandler{builder: builder}
}

// Cancel handles DELETE /chats/:chatId/queue/:generationId.
func (h *QueueHandler) Cancel(c *gin.Context) {
	err := h.builder.CancelQueuedGeneration(c.Request.Context(), auth.TenantID(c), c.Param("chatId"), c.Param("generationId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.NoContent(c)
}

// CancelAll handles DELETE /chats/:chatId/queue — cancels the running
// generation (if any) and everything still queued behind it, in one call.
// See themebuild.Service.CancelAllPending's doc comment.
func (h *QueueHandler) CancelAll(c *gin.Context) {
	err := h.builder.CancelAllPending(c.Request.Context(), auth.TenantID(c), c.Param("chatId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.NoContent(c)
}
