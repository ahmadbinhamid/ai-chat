package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// QueueHandler exposes cancelling a still-queued prompt — see
// themebuild.Service.CancelQueuedGeneration's doc comment for what "cancel"
// does and doesn't cover (never a running generation).
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
