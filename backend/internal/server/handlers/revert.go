package handlers

import (
	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// RevertHandler exposes reverting a chat's theme back to a past turn — see
// themebuild.Service.RevertToMessage's doc comment for what "revert" means
// in ai-chat's own (HTTP-only, no local disk) architecture.
type RevertHandler struct {
	builder *themebuild.Service
}

func NewRevertHandler(builder *themebuild.Service) *RevertHandler {
	return &RevertHandler{builder: builder}
}

// Revert handles POST /chats/:chatId/messages/:messageId/revert.
func (h *RevertHandler) Revert(c *gin.Context) {
	result, err := h.builder.RevertToMessage(c.Request.Context(), auth.TenantID(c), auth.Token(c),
		c.Param("chatId"), c.Param("messageId"))
	if err != nil {
		respondErr(c, err)
		return
	}
	httpresponse.OK(c, result)
}
