package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// QueueHandler exposes cancelling a prompt, queued or already running; a queued prompt
// stops synchronously, a running one stops asynchronously once its goroutine notices.
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

// CancelAll handles DELETE /chats/:chatId/queue — cancels the running generation (if any) and everything queued behind it.
func (h *QueueHandler) CancelAll(c *gin.Context) {
	err := h.builder.CancelAllPending(c.Request.Context(), auth.TenantID(c), c.Param("chatId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.NoContent(c)
}
